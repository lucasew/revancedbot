package cli

import (
	"fmt"

	"github.com/lucasew/revancedbot/internal/signing"
	"github.com/lucasew/revancedbot/internal/toolscheck"
	"github.com/lucasew/workspaced/pkg/logging"
	"github.com/spf13/cobra"
)

var (
	checkKeyTools = func() error { return toolscheck.Check(toolscheck.KeysOnly()) }
	generateKeys  = signing.Generate
)

func newKeysCmd() *cobra.Command {
	cmd := &cobra.Command{
		Use:   "keys",
		Short: "Signing key helpers",
	}
	cmd.AddCommand(newKeysGenerateCmd(), newKeysValidateCmd())
	return cmd
}

func newKeysGenerateCmd() *cobra.Command {
	return &cobra.Command{
		Use:   "generate",
		Short: "Generate a keystore and print one pasteable signing secret",
		Args:  cobra.NoArgs,
		RunE: func(cmd *cobra.Command, args []string) error {
			if err := checkKeyTools(); err != nil {
				return err
			}
			// Intentionally no taskgroup.Go: starting the bubbletea Session UI for a
			// short keytool run races on teardown (deadlock) and can corrupt the
			// pasteable secret on stdout. Session still Enter/Close with zero tasks.
			ctx := ctxOf(cmd)
			log := logging.GetLogger(ctx)
			enc, err := generateKeys("")
			if err != nil {
				return err
			}
			// Machine contract: exactly one line on stdout (the secret blob).
			fmt.Println(enc)
			log.Info("paste the line above into the REVANCEDBOT_SIGNING secret")
			return nil
		},
	}
}

func newKeysValidateCmd() *cobra.Command {
	return &cobra.Command{
		Use:   "validate REPO",
		Short: "Validate REVANCEDBOT_SIGNING and materialize keystore into CACHE",
		Args:  cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			if err := toolscheck.Check(toolscheck.KeysOnly()); err != nil {
				return err
			}
			a, err := loadApp(args, loadOpts{})
			if err != nil {
				return err
			}
			ctx := ctxOf(cmd)
			log := logging.GetLogger(ctx)
			// Sync path: no Go/Unit — avoid TUI teardown flake on short work.
			if err := a.LoadSigning(); err != nil {
				return err
			}
			log.Info("signing blob valid", "keystore", a.WS.KeystorePath, "cache", a.WS.Cache)
			return nil
		},
	}
}
