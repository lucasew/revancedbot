package cli

import (
	"context"
	"fmt"

	"github.com/lucasew/revancedbot/internal/app"
	"github.com/lucasew/revancedbot/internal/download"
	"github.com/lucasew/revancedbot/internal/osx"
	"github.com/lucasew/revancedbot/internal/workspace"
	"github.com/lucasew/workspaced/pkg/logging"
	"github.com/lucasew/workspaced/pkg/taskgroup"
	"github.com/spf13/cobra"
)

var checkStockIdentity = app.CheckStockIdentity

func newDownloadCmd() *cobra.Command {
	var pkg, ver string
	c := &cobra.Command{
		Use:   "download REPO",
		Short: "Download one stock APK into CACHE",
		Args:  cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			a, err := loadApp(args, loadOpts{requireDoc: true})
			if err != nil {
				return err
			}
			if pkg == "" {
				return fmt.Errorf("--package is required: %w", ErrBase)
			}
			pkg = download.CanonicalPackage(pkg)
			ctx := ctxOf(cmd)
			log := logging.GetLogger(ctx)
			return schedule(ctx, "download stock "+pkg, taskgroup.Control, func(ctx context.Context, s *taskgroup.Status) error {
				s.Update(pkg)
				path := a.WS.StockAPKPath(pkg, ver)
				if workspace.CacheHit(path) && download.AcceptCached(path) == nil {
					if err := checkStockIdentity(path, pkg, ver); err == nil {
						log.Info("stock cache hit", "path", path)
						fmt.Printf("cache\t%s\n", path)
						return nil
					}
					log.Warn("stock cache identity rejected", "path", path)
					osx.Remove(path)
				}
				reg := download.DefaultRegistry(a.Cfg.BrowserCDPURL)
				order := a.Cfg.DownloaderOrder
				if len(order) == 0 {
					order = download.DefaultOrder
				}
				res, err := download.FetchFirst(ctx, reg, order, download.Request{
					PackageID: pkg,
					Version:   ver,
				}, a.WS.StockAPKs)
				if err != nil {
					return err
				}
				if err := checkStockIdentity(res.Path, pkg, ver); err != nil {
					osx.Remove(res.Path)
					return err
				}
				log.Info("download ok", "source", res.SourceID, "sha256", res.SHA256, "path", res.Path)
				fmt.Printf("%s\t%s\t%s\n", res.SourceID, res.SHA256, res.Path)
				return nil
			})
		},
	}
	c.Flags().StringVar(&pkg, "package", "", "stock package id")
	c.Flags().StringVar(&ver, "version", "", "version (empty = latest)")
	return c
}
