package forum

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"strings"

	"github.com/alijnmerchant21/forum-updated/model"
	"github.com/cometbft/cometbft/abci/types"
	abci "github.com/cometbft/cometbft/abci/types"
	"github.com/cometbft/cometbft/crypto/ed25519"
	cryptoencoding "github.com/cometbft/cometbft/crypto/encoding"
	cryptoproto "github.com/cometbft/cometbft/proto/tendermint/crypto"

	"github.com/cometbft/cometbft/version"
	"github.com/dgraph-io/badger/v3"
)

const (
	CodeTypeOK              uint32 = 0
	CodeTypeEncodingError   uint32 = 1
	CodeTypeInvalidTxFormat uint32 = 2
	CodeTypeBanned          uint32 = 3
)

const ApplicationVersion = 1

type ForumApp struct {
	abci.BaseApplication
	User               *model.User
	DB                 *model.DB
	Msg                *model.Message
	Height             int64
	valAddrToPubKeyMap map[string]cryptoproto.PublicKey

	stagedTxs    [][]byte
	stagedBanTxs [][]byte
}

func NewForumApp(dbDir string) (*ForumApp, error) {
	db, err := model.NewDB(dbDir)
	if err != nil {
		fmt.Printf("Error initializing database: %s\n", err)
		return nil, err
	}

	user := &model.User{}
	//db := &model.DB{}

	return &ForumApp{
		User:               user,
		DB:                 db,
		stagedTxs:          make([][]byte, 0),
		stagedBanTxs:       make([][]byte, 0),
		valAddrToPubKeyMap: make(map[string]cryptoproto.PublicKey),
	}, nil
}

// Return application info
func (app ForumApp) Info(_ context.Context, info *abci.RequestInfo) (*abci.ResponseInfo, error) {
	if len(app.valAddrToPubKeyMap) == 0 && app.Height > 0 {
		validators := app.getValidators()
		for _, v := range validators {
			pubkey, err := cryptoencoding.PubKeyFromProto(v.PubKey)
			if err != nil {
				panic(fmt.Errorf("can't decode public key: %w", err))
			}
			app.valAddrToPubKeyMap[string(pubkey.Address())] = v.PubKey
		}
	}
	return &abci.ResponseInfo{
		Version:         version.ABCIVersion,
		AppVersion:      ApplicationVersion,
		LastBlockHeight: app.Height,
	}, nil
}

// Query blockchain
func (app ForumApp) Query(ctx context.Context, query *abci.RequestQuery) (*abci.ResponseQuery, error) {
	resp := abci.ResponseQuery{Key: query.Data}

	// Just testing whether validators are properly stored
	if query.Path == "/val" {
		for k := range app.valAddrToPubKeyMap {
			return &types.ResponseQuery{
				Key:   query.Data,
				Value: []byte(string(k)),
			}, nil
		}
	}
	// Parse sender from query data
	sender := string(query.Data)

	if sender == "history" {
		messages, err := model.FetchHistory(app.DB)
		if err != nil {
			fmt.Println("Error fetching history")
			return nil, err
		}
		if err != nil {
			fmt.Println("error unmarshalling when fetching:", err)
		}

		resp.Log = messages
		resp.Value = []byte(messages)

		return &resp, nil
	}
	// Retrieve all message sent by the sender
	messages, err := model.GetMessagesBySender(app.DB, sender)
	if err != nil {
		fmt.Println("Error in query1")
		return nil, err
	}

	// Convert the messages to JSON and return as query result
	resultBytes, err := json.Marshal(messages)
	if err != nil {
		fmt.Println("Error in query")
		return nil, err
	}

	resp.Log = string(resultBytes)
	resp.Value = resultBytes

	return &resp, nil
}

func (app ForumApp) CheckTx(ctx context.Context, checktx *abci.RequestCheckTx) (*abci.ResponseCheckTx, error) {

	// Parse the tx message
	msg, err := model.ParseMessage(checktx.Tx)
	if err != nil {
		fmt.Printf("failed to parse transaction message checktx: %v\n", err)
		return &abci.ResponseCheckTx{Code: CodeTypeInvalidTxFormat, Log: "Invalid transaction format"}, err
	}
	fmt.Println("Searching for sender ... ", msg.Sender)
	u, err := app.DB.FindUserByName(msg.Sender)

	if err != nil && !errors.Is(err, badger.ErrKeyNotFound) {
		fmt.Println("problem in check tx: ", string(checktx.Tx))
		return &types.ResponseCheckTx{Code: CodeTypeEncodingError}, nil
	}

	if err == badger.ErrKeyNotFound {
		fmt.Println("Not found user :", msg.Sender)
	}
	if u != nil {
		fmt.Println(u)
	}
	if u != nil && u.Banned {
		fmt.Println("User is banned")
		return &types.ResponseCheckTx{Code: CodeTypeBanned, Log: "User is banned"}, nil
	}
	fmt.Println("Check tx success for ", msg.Message, " and ", msg.Sender)
	return &types.ResponseCheckTx{Code: CodeTypeOK}, nil
}

// Consensus Connection
// Initialize blockchain w validators/other info from CometBFT
func (app ForumApp) InitChain(_ context.Context, req *abci.RequestInitChain) (*abci.ResponseInitChain, error) {
	for _, v := range req.Validators {
		app.updateValidator(v)
	}
	CurseWords := []string{"fuck", "shit", "asshole", "bitch"}
	for _, curseWord := range CurseWords {
		err := app.DB.AddCurseWords(curseWord)
		if err != nil {
			return &abci.ResponseInitChain{}, err
		}
	}
	req.ConsensusParams.Abci.VoteExtensionsEnableHeight = 1
	return &abci.ResponseInitChain{ConsensusParams: req.ConsensusParams}, nil
}

func (app *ForumApp) PrepareProposal(_ context.Context, proposal *abci.RequestPrepareProposal) (*abci.ResponsePrepareProposal, error) {
	fmt.Println("entered prepareProp")

	voteExtensions := proposal.LocalLastCommit.Votes
	curseWordMap := make(map[string]int)
	for _, vote := range voteExtensions {
		// TODO verify the extension signature
		if _, ok := app.valAddrToPubKeyMap[string(vote.GetValidator().Address)]; !ok {
			// We do not have this validator, should be an error too
			continue
		}

		// This code gets the curse words and makes sure that we do not add them more than once
		// Thus ensuring each validator only adds one word once

		tmpCurseWordMap := make(map[string]struct{})
		curseWords := strings.Split(string(vote.GetVoteExtension()), "|")

		// TODO This should be moved to verify vote extension so we don't do it here
		for _, word := range curseWords {
			tmpCurseWordMap[word] = struct{}{}
		}
		for word := range tmpCurseWordMap {
			if count, ok := curseWordMap[word]; !ok {
				curseWordMap[word] = 1
			} else {
				curseWordMap[word] = count + 1
			}
		}

	}
	fmt.Println("Processed vote extensions :", curseWordMap)
	majority := len(app.valAddrToPubKeyMap) / 2 // We define the majority to be at least half of the validators;
	// has to be at least 1/3

	voteExtensionCurseWords := ""
	for word, count := range curseWordMap {
		if count > majority {
			voteExtensionCurseWords = voteExtensionCurseWords + "|" + word
		}
	}

	// prepare proposal puts the BanTx first, then adds the other transactions
	// ProcessProposal should verify this
	proposedTxs := make([][]byte, 0)
	finalProposal := make([][]byte, 0)
	bannedUsersString := make(map[string]struct{})
	for _, tx := range proposal.Txs {
		msg, err := model.ParseMessage(tx)
		if err == nil {
			curseWords, err := app.DB.GetCurseWords()

			if err != nil {
				proposedTxs = append(proposedTxs, tx)
			}
			// Adding the curse words from vote extensions too
			if !model.IsCurseWord(msg.Message, curseWords+voteExtensionCurseWords) {
				proposedTxs = append(proposedTxs, tx)
			} else {
				banTx := model.BanTx{UserName: msg.Sender}
				bannedUsersString[msg.Message] = struct{}{}
				resultBytes, err := json.Marshal(banTx)
				if err == nil {
					finalProposal = append(finalProposal, resultBytes)
				}
			}
		}
	}
	// Need to loop again through the proposed Txs to make sure there is none left by a user that was banned after the tx was accepted
	for _, tx := range proposedTxs {
		// there should be no error here as these are just transactions we have checked and added
		msg, _ := model.ParseMessage(tx)
		if _, ok := bannedUsersString[msg.Sender]; !ok {
			finalProposal = append(finalProposal, tx)
		}
	}
	return &types.ResponsePrepareProposal{Txs: finalProposal}, nil
}

func (ForumApp) ProcessProposal(_ context.Context, processproposal *abci.RequestProcessProposal) (*abci.ResponseProcessProposal, error) {
	fmt.Println("entered processProp")
	processedBanTxs := false
	bannedUsers := make(map[string]struct{}, 0)
	for _, tx := range processproposal.Txs {
		var parsedBan model.BanTx
		var parsedTx model.Message
		var err error
		if !processedBanTxs && isBanTx(tx) {
			fmt.Println("FoundBanTx")
			err = json.Unmarshal(tx, &parsedBan)
			if err != nil {
				return &types.ResponseProcessProposal{Status: types.ResponseProcessProposal_REJECT}, err
			}
			fmt.Println("Banned user found", string(tx))
			if processedBanTxs {
				// Banning transactions have to come first, cannot have them once we hit the first non user ban tx
				return &types.ResponseProcessProposal{Status: types.ResponseProcessProposal_REJECT}, err
			}
			bannedUsers[parsedBan.UserName] = struct{}{}
		} else {
			_, err = model.ParseMessage(tx)
			if err != nil {
				fmt.Println(string(tx))
				return &types.ResponseProcessProposal{Status: types.ResponseProcessProposal_REJECT}, err
			}
			processedBanTxs = true
			if _, ok := bannedUsers[parsedTx.Sender]; ok {
				// sending us a tx from a banned user
				return &types.ResponseProcessProposal{Status: types.ResponseProcessProposal_REJECT}, err
			}
		}
	}
	return &types.ResponseProcessProposal{Status: types.ResponseProcessProposal_ACCEPT}, nil
}

// Deliver the decided block with its txs to the Application
func (app *ForumApp) FinalizeBlock(_ context.Context, req *abci.RequestFinalizeBlock) (*abci.ResponseFinalizeBlock, error) {
	fmt.Println("entered finalizeBlock")
	// Iterate over Tx in current block
	app.stagedBanTxs = make([][]byte, 0)
	app.stagedTxs = make([][]byte, 0)
	respTxs := make([]*types.ExecTxResult, len(req.Txs))
	for i, tx := range req.Txs {
		var err error
		//Check if it's a banning transaction
		if isBanTx(tx) {
			banTx := new(model.BanTx)
			err = json.Unmarshal(tx, &banTx)
			if err != nil {
				respTxs[i] = &types.ExecTxResult{Code: CodeTypeEncodingError}
			} else {
				respTxs[i] = &types.ExecTxResult{Code: CodeTypeOK}
				app.stagedBanTxs = append(app.stagedBanTxs, tx)
			}

		} else {
			_, err := model.ParseMessage(tx)
			if err != nil {
				respTxs[i] = &types.ExecTxResult{Code: CodeTypeEncodingError}
			} else {
				app.stagedTxs = append(app.stagedTxs, tx)
				// This adds the user to the DB, but the data is not committed nor persisted until Comit is called
				respTxs[i] = &types.ExecTxResult{Code: abci.CodeTypeOK}
			}
		}
	}
	app.Height = req.Height
	response := &abci.ResponseFinalizeBlock{TxResults: respTxs}
	return response, nil
}

// Commit the state and return the application Merkle root hash
// Here we actually write the staged transactions into the database.
// For details on why it has to be done here, check the Crash recovery section
// of the ABCI spec
func (app ForumApp) Commit(_ context.Context, commit *abci.RequestCommit) (*abci.ResponseCommit, error) {
	banTx := new(model.BanTx)
	for _, tx := range app.stagedBanTxs {
		err := json.Unmarshal(tx, &banTx)
		if err != nil {
			return nil, err
		}
		u, err := app.DB.FindUserByName(banTx.UserName)
		if err == nil {
			u.Banned = true
			err = app.DB.UpdateUser(*u)
			if err != nil {
				fmt.Println("Error updating user :", err)
				return nil, err
			}
		} else {
			if errors.Is(err, badger.ErrKeyNotFound) {
				fmt.Println("User is not found, creating it ")
				newUser := model.User{
					Name:      banTx.UserName,
					PubKey:    ed25519.GenPrivKey().PubKey().Bytes(),
					Moderator: false,
					Banned:    true,
				}
				err := app.DB.CreateUser(&newUser)
				if err != nil {
					return nil, err
				}
				u = &newUser
			} else {
				return nil, err
			}
		}

	}
	fmt.Println("Commit banned txs succeeded")

	for _, tx := range app.stagedTxs {
		msg, err := model.ParseMessage(tx)
		if err != nil {
			return nil, err
		} else {
			_, err := app.DB.FindUserByName(msg.Sender)
			if errors.Is(err, badger.ErrKeyNotFound) {
				newUser := model.User{
					Name:      msg.Sender,
					PubKey:    ed25519.GenPrivKey().PubKey().Bytes(),
					Moderator: false,
					Banned:    false,
				}
				err := app.DB.CreateUser(&newUser)
				if err != nil {
					return nil, err
				}
			}
			err = model.AddMessage(app.DB, *msg)
			if err != nil {
				return nil, err
			}
		}
	}

	app.DB.GetDB().Sync()
	return &abci.ResponseCommit{}, nil
}

// State Sync Connection
// List available snapshots
func (ForumApp) ListSnapshots(_ context.Context, listsnapshot *abci.RequestListSnapshots) (*abci.ResponseListSnapshots, error) {
	return &abci.ResponseListSnapshots{}, nil
}

func (ForumApp) OfferSnapshot(_ context.Context, offersnapshot *abci.RequestOfferSnapshot) (*abci.ResponseOfferSnapshot, error) {
	return &abci.ResponseOfferSnapshot{}, nil
}

func (ForumApp) LoadSnapshotChunk(_ context.Context, loadsnapshotchunk *abci.RequestLoadSnapshotChunk) (*abci.ResponseLoadSnapshotChunk, error) {
	return &abci.ResponseLoadSnapshotChunk{}, nil
}

func (ForumApp) ApplySnapshotChunk(_ context.Context, applysnapshotchunk *abci.RequestApplySnapshotChunk) (*abci.ResponseApplySnapshotChunk, error) {
	return &abci.ResponseApplySnapshotChunk{}, nil
}

func (app ForumApp) ExtendVote(_ context.Context, extendvote *abci.RequestExtendVote) (*abci.ResponseExtendVote, error) {
	fmt.Println("Entered extend vote")
	curseWords, err := app.DB.GetCurseWords()
	if err != nil {
		fmt.Println("Failed to extend vote")
		return &abci.ResponseExtendVote{}, nil
	}

	return &abci.ResponseExtendVote{VoteExtension: []byte(curseWords)}, nil
}

func (app ForumApp) VerifyVoteExtension(_ context.Context, req *abci.RequestVerifyVoteExtension) (*abci.ResponseVerifyVoteExtension, error) {
	fmt.Println("Entered verify extension") // Will not be called for extensions generated by this validator
	if _, ok := app.valAddrToPubKeyMap[string(req.ValidatorAddress)]; !ok {
		// we do not have a validator with this address mapped
		return &abci.ResponseVerifyVoteExtension{Status: abci.ResponseVerifyVoteExtension_REJECT}, nil
	}

	return &abci.ResponseVerifyVoteExtension{Status: abci.ResponseVerifyVoteExtension_ACCEPT}, nil
}

func isBanTx(tx []byte) bool {
	return strings.Contains(string(tx), "username")
}

func (app *ForumApp) updateValidator(v types.ValidatorUpdate) {
	pubkey, err := cryptoencoding.PubKeyFromProto(v.PubKey)
	if err != nil {
		panic(fmt.Errorf("can't decode public key: %w", err))
	}
	key := []byte("val" + string(pubkey.Bytes()))

	// add or update validator
	value := bytes.NewBuffer(make([]byte, 0))
	if err := types.WriteMessage(&v, value); err != nil {
		panic(err)
	}
	if err = app.DB.Set(key, value.Bytes()); err != nil {
		panic(err)
	}
	app.valAddrToPubKeyMap[string(pubkey.Address())] = v.PubKey

}

func (app *ForumApp) getValidators() (validators []types.ValidatorUpdate) {

	err := app.DB.GetValidators(validators)
	if err != nil {
		panic(err)
	}
	return
}
