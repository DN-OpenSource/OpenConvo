package main

import (
	"context"
	"fmt"
	"net"
	"net/http"
	"time"

	"github.com/spf13/cobra"

	"github.com/openstream/openstream/internal/auth"
	"github.com/openstream/openstream/internal/config"
	"github.com/openstream/openstream/internal/store"
)

func migrateCmd() *cobra.Command {
	cmd := &cobra.Command{
		Use:   "migrate [up|down]",
		Short: "Apply or revert database migrations",
		Args:  cobra.MaximumNArgs(1),
		RunE: func(_ *cobra.Command, args []string) error {
			cfg, err := config.Load(configPath)
			if err != nil {
				return err
			}
			direction := "up"
			if len(args) == 1 {
				direction = args[0]
			}
			switch direction {
			case "up":
				if err := store.MigrateUp(cfg.PostgresDSN); err != nil {
					return err
				}
				fmt.Println("migrations applied")
			case "down":
				if err := store.MigrateDown(cfg.PostgresDSN); err != nil {
					return err
				}
				fmt.Println("migrations reverted")
			default:
				return fmt.Errorf("unknown direction %q (up|down)", direction)
			}
			return nil
		},
	}
	return cmd
}

func appCmd() *cobra.Command {
	cmd := &cobra.Command{
		Use:   "app",
		Short: "Manage apps (tenants) on this cluster",
	}

	create := &cobra.Command{
		Use:   "create <name>",
		Short: "Create an app and print its API credentials",
		Args:  cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			st, err := openStore(cmd.Context())
			if err != nil {
				return err
			}
			defer st.Close()
			app, err := st.CreateApp(cmd.Context(), args[0])
			if err != nil {
				return err
			}
			fmt.Printf("app_id:     %s\napi_key:    %s\napi_secret: %s\n", app.ID, app.APIKey, app.APISecret)
			return nil
		},
	}

	list := &cobra.Command{
		Use:   "list",
		Short: "List apps",
		RunE: func(cmd *cobra.Command, _ []string) error {
			st, err := openStore(cmd.Context())
			if err != nil {
				return err
			}
			defer st.Close()
			apps, err := store.ListApps(cmd.Context(), st.Pool)
			if err != nil {
				return err
			}
			for _, app := range apps {
				fmt.Printf("%s  %s  api_key=%s\n", app.ID, app.Name, app.APIKey)
			}
			return nil
		},
	}

	cmd.AddCommand(create, list)
	return cmd
}

func tokenCmd() *cobra.Command {
	var (
		appKey string
		userID string
		server bool
		exp    time.Duration
	)
	cmd := &cobra.Command{
		Use:   "token",
		Short: "Mint a user or server JWT for an app (SPEC.md §10)",
		RunE: func(cmd *cobra.Command, _ []string) error {
			if appKey == "" {
				return fmt.Errorf("--app (api_key) is required")
			}
			st, err := openStore(cmd.Context())
			if err != nil {
				return err
			}
			defer st.Close()
			app, err := store.GetAppByKey(cmd.Context(), st.Pool, appKey)
			if err != nil {
				return err
			}
			var token string
			switch {
			case server:
				token, err = auth.MintServerToken(app.APISecret)
			case userID != "":
				token, err = auth.MintUserToken(app.APISecret, userID, exp)
			default:
				return fmt.Errorf("--user or --server is required")
			}
			if err != nil {
				return err
			}
			fmt.Println(token)
			return nil
		},
	}
	cmd.Flags().StringVar(&appKey, "app", "", "app api_key")
	cmd.Flags().StringVar(&userID, "user", "", "user id to mint for")
	cmd.Flags().BoolVar(&server, "server", false, "mint a server token")
	cmd.Flags().DurationVar(&exp, "exp", 0, "token lifetime (0 = no expiry)")
	return cmd
}

func doctorCmd() *cobra.Command {
	return &cobra.Command{
		Use:   "doctor",
		Short: "Diagnose environment connectivity (postgres, redis, nats)",
		RunE: func(cmd *cobra.Command, _ []string) error {
			cfg, err := config.Load(configPath)
			if err != nil {
				return err
			}
			ok := true
			check := func(name string, fn func() error) {
				if err := fn(); err != nil {
					ok = false
					fmt.Printf("✗ %-10s %v\n", name, err)
					return
				}
				fmt.Printf("✓ %-10s ok\n", name)
			}
			ctx, cancel := context.WithTimeout(cmd.Context(), 5*time.Second)
			defer cancel()
			check("postgres", func() error {
				st, err := store.New(ctx, cfg.PostgresDSN)
				if err != nil {
					return err
				}
				st.Close()
				return nil
			})
			check("redis", func() error { return dialTCP(ctx, cfg.RedisAddr) })
			check("nats", func() error {
				u := cfg.NATSURL
				if after, found := cutPrefix(u, "nats://"); found {
					u = after
				}
				return dialTCP(ctx, u)
			})
			check("http", func() error {
				req, err := http.NewRequestWithContext(ctx, http.MethodGet, "http://localhost"+cfg.HTTPAddr+"/healthz", nil)
				if err != nil {
					return err
				}
				resp, err := http.DefaultClient.Do(req)
				if err != nil {
					return fmt.Errorf("server not running locally (fine if deployed elsewhere)")
				}
				_ = resp.Body.Close()
				return nil
			})
			if !ok {
				return fmt.Errorf("one or more checks failed")
			}
			return nil
		},
	}
}

func openStore(ctx context.Context) (*store.Store, error) {
	cfg, err := config.Load(configPath)
	if err != nil {
		return nil, err
	}
	return store.New(ctx, cfg.PostgresDSN)
}

func dialTCP(ctx context.Context, addr string) error {
	dialer := &net.Dialer{Timeout: 3 * time.Second}
	conn, err := dialer.DialContext(ctx, "tcp", addr)
	if err != nil {
		return err
	}
	return conn.Close()
}

func cutPrefix(s, prefix string) (string, bool) {
	if len(s) >= len(prefix) && s[:len(prefix)] == prefix {
		return s[len(prefix):], true
	}
	return s, false
}
