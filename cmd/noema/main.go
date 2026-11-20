package main

import (
	"context"
	"errors"
	"fmt"
	"os"
	"os/signal"
	"syscall"

	"github.com/CesarBraz7/noema/internal/pipeline"
)

const version = "0.1.0"

func main() {
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()
	if err := run(ctx, os.Args[1:]); err != nil && !errors.Is(err, context.Canceled) {
		fmt.Fprintln(os.Stderr, "noema:", err)
		os.Exit(1)
	}
}

func run(ctx context.Context, args []string) error {
	switch {
	case len(args) == 0 || len(args) == 1 && (args[0] == "--help" || args[0] == "help"):
		fmt.Println("Uso: noema {version|migrate|once|run}")
		return nil
	case len(args) != 1:
		return errors.New("uso: noema {version|migrate|once|run}")
	}
	switch args[0] {
	case "version":
		fmt.Println("noema", version)
		return nil
	case "migrate":
		return pipeline.Migrate(ctx, databaseURL())
	case "once", "run":
		app, err := pipeline.FromEnv(ctx, databaseURL())
		if err != nil {
			return err
		}
		if args[0] == "once" {
			return app.Cycle(ctx, true, true)
		}
		app.Run(ctx)
		return nil
	default:
		return fmt.Errorf("comando inválido %q. Use noema --help", args[0])
	}
}

// River needs a direct Postgres session. Neon supplies both URLs by default.
func databaseURL() string {
	if direct := os.Getenv("DATABASE_URL_UNPOOLED"); direct != "" {
		return direct
	}
	return os.Getenv("DATABASE_URL")
}
