package main

import (
	"context"
	"flag"
	"fmt"
	app "github.com/cometbft/abci-v2-forum-app/abci"
	"log"
	"net/http"
	"os"
	"os/signal"
	"path/filepath"
	"syscall"

	"github.com/cometbft/cometbft/p2p"
	"github.com/cometbft/cometbft/proxy"
	"github.com/spf13/viper"

	db "github.com/cometbft/cometbft-db"
	cfg "github.com/cometbft/cometbft/config"
	cmtflags "github.com/cometbft/cometbft/libs/cli/flags"
	cmtlog "github.com/cometbft/cometbft/libs/log"
	nm "github.com/cometbft/cometbft/node"
	"github.com/cometbft/cometbft/privval"
)

var homeDir string

func init() {
	flag.StringVar(&homeDir, "home", "", "Path to the CometBFT config directory (if empty, uses $HOME/.forumapp)")
}

func main() {
	flag.Parse()
	if homeDir == "" {
		homeDir = os.ExpandEnv("$HOME/.forumapp")
	}

	config := cfg.DefaultConfig()
	config.SetRoot(homeDir)
	viper.SetConfigFile(fmt.Sprintf("%s/%s", homeDir, "config/config.toml"))

	if err := viper.ReadInConfig(); err != nil {
		log.Fatalf("failed to read config: %v", err)
	}

	store, err := db.NewGoLevelDB(filepath.Join(homeDir, "forum-db"), ".")
	if err != nil {
		log.Fatalf("failed to create database: %v", err)
	}
	defer store.Close()

	dbPath := "forum-db"
	appConfigPath := "app.toml"
	app, err := app.NewForumApp(dbPath, appConfigPath)

	if err != nil {
		log.Fatalf("failed to create ForumApp instance: %v", err)
	}

	logger := cmtlog.NewTMLogger(cmtlog.NewSyncWriter(os.Stdout))
	logger, err = cmtflags.ParseLogLevel(config.LogLevel, logger, cfg.DefaultLogLevel)

	if err != nil {
		log.Fatalf("failed to read genesis doc: %v", err)
	}

	nodeKey, err := p2p.LoadNodeKey(config.NodeKeyFile())
	if err != nil {
		log.Fatalf("failed to load node key: %v", err)
	}

	pv := privval.LoadFilePV(
		config.PrivValidatorKeyFile(),
		config.PrivValidatorStateFile(),
	)

	node, err := nm.NewNode(
		context.Background(),
		config,
		pv,
		nodeKey,
		proxy.NewLocalClientCreator(app),
		nm.DefaultGenesisDocProviderFunc(config),
		cfg.DefaultDBProvider,
		nm.DefaultMetricsProvider(config.Instrumentation),
		logger,
	)

	if err != nil {
		log.Fatalf("failed to create CometBFT node: %v", err)
	}

	if err := node.Start(); err != nil {
		log.Fatalf("failed to start CometBFT node: %v", err)
	}
	defer func() {
		_ = node.Stop()
		node.Wait()
	}()

	httpAddr := "127.0.0.1:8080"

	if err := http.ListenAndServe(httpAddr, nil); err != nil {
		log.Fatalf("failed to start HTTP server: %v", err)
	}

	sigCh := make(chan os.Signal, 1)
	signal.Notify(sigCh, syscall.SIGINT, syscall.SIGTERM)
	<-sigCh

	fmt.Println("Forum application stopped")
}
