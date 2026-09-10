package app

import (
	"context"
	"errors"
	"fmt"
	"math/rand"
	"os"
	"path/filepath"
	"strings"
	"sync/atomic"

	"github.com/lucasew/revancedbot/internal/apkmeta"
	"github.com/lucasew/revancedbot/internal/config"
	"github.com/lucasew/revancedbot/internal/download"
	"github.com/lucasew/revancedbot/internal/fdroid"
	"github.com/lucasew/revancedbot/internal/netx"
	"github.com/lucasew/revancedbot/internal/osx"
	"github.com/lucasew/revancedbot/internal/revanced"
	"github.com/lucasew/revancedbot/internal/signing"
	"github.com/lucasew/revancedbot/internal/strutil"
	"github.com/lucasew/revancedbot/internal/toolscheck"
	"github.com/lucasew/revancedbot/internal/workspace"
	"github.com/lucasew/workspaced/pkg/logging"
	"github.com/lucasew/workspaced/pkg/taskgroup"
)

// App wires config + layout for CLI commands.
type App struct {
	Cfg  *config.Config
	WS   *workspace.Layout
	Blob *signing.Blob
}

// New builds layout from config (REPO + CACHE).
func New(cfg *config.Config) (*App, error) {
	ws, err := workspace.New(cfg.Repo, cfg.Cache)
	if err != nil {
		return nil, err
	}
	if err := ws.Ensure(); err != nil {
		return nil, err
	}
	cfg.Cache = ws.Cache
	return &App{Cfg: cfg, WS: ws}, nil
}

// LoadSigning materializes the signing blob into CACHE.
func (a *App) LoadSigning() error {
	if a.Cfg.SigningBlob == "" {
		return fmt.Errorf("REVANCEDBOT_SIGNING is required: %w", ErrBase)
	}
	blob, err := signing.DecodeBlob(a.Cfg.SigningBlob)
	if err != nil {
		return err
	}
	if err := blob.Materialize(a.WS.Cache); err != nil {
		return err
	}
	a.Blob = blob
	return nil
}

// PrepareStage seeds CACHE/fdroid from live REPO (history) and writes stage config.yml.
// No live REPO mutation.
func (a *App) PrepareStage() error {
	if a.Blob == nil {
		return fmt.Errorf("signing not loaded: %w", ErrBase)
	}
	if err := fdroid.SeedStage(a.WS.Stage, a.WS.Repo); err != nil {
		return err
	}
	return fdroid.WriteConfig(a.WS.StageConfig(), fdroid.RepoMeta{
		Name:        a.Cfg.RepoName,
		URL:         a.Cfg.RepoURL,
		Description: a.Cfg.RepoDescription,
	}, a.WS.Cache, a.WS.Repo, a.WS.KeystorePath, a.Blob)
}

// WriteFDroidConfig is an alias for PrepareStage (stage-only config).
func (a *App) WriteFDroidConfig() error {
	return a.PrepareStage()
}

// PublishStage atomically replaces REPO/{repo,metadata,config.yml} from CACHE/fdroid.
func (a *App) PublishStage() error {
	return fdroid.Publish(fdroid.PublishArgs{Stage: a.WS.Stage, Live: a.WS.Repo})
}

// FetchTools downloads CLI + patches into CACHE.
// A patches file/URL override replaces a cached .rvp even on name-hits.
func (a *App) FetchTools(ctx context.Context) error {
	log := logging.GetLogger(ctx)
	cli := a.WS.PatcherJAR()
	rvp := a.WS.PatchesRVP()
	override := revanced.PatchesOverride()
	if workspace.CacheHit(cli) && workspace.CacheHit(rvp) && !override {
		log.Info("tools cache hit", "cli", cli, "patches", rvp)
		return nil
	}
	log.Info("fetching ReVanced CLI and patches into cache", "cache", a.WS.Cache)
	if override && workspace.CacheHit(cli) {
		return revanced.FetchPatches(ctx, a.Cfg.GitHubToken, rvp)
	}
	return revanced.FetchLatest(ctx, a.Cfg.GitHubToken, cli, rvp)
}

// ListJobs returns patch jobs.
func (a *App) ListJobs() ([]revanced.Job, error) {
	if !workspace.CacheHit(a.WS.PatcherJAR()) {
		return nil, fmt.Errorf("missing CLI jar in cache; run fetch-tools first: %s: %w", a.WS.PatcherJAR(), ErrBase)
	}
	if !workspace.CacheHit(a.WS.PatchesRVP()) {
		return nil, fmt.Errorf("missing patches in cache; run fetch-tools first: %s: %w", a.WS.PatchesRVP(), ErrBase)
	}
	return revanced.ListJobs("java", a.WS.PatcherJAR(), a.WS.PatchesRVP())
}

// ProcessPackage downloads and patches one package (version walk).
// No nested Control Maps — progress for stock APKs is owned by the parent
// "apks" Map in RunFull (plus httpclient fetch bars for network).
func (a *App) ProcessPackage(ctx context.Context, job revanced.Job) error {
	log := logging.GetLogger(ctx)
	reg := a.stockRegistry()
	order := a.Cfg.DownloaderOrder
	if len(order) == 0 {
		order = download.DefaultOrder
	}

	err := tryVersions(job.Versions, func(ver string) error {
		err := a.processVersion(ctx, job, ver, reg, order)
		if err != nil {
			log.Warn("version failed", "package", job.PackageID, "version", emptyAsLatest(ver), "err", err)
		}
		return err
	})
	if err != nil {
		return fmt.Errorf("skip %s: %w", job.PackageID, err)
	}
	return nil
}

// tryVersions walks preferred versions. A nil result wins. errStopWalk ends
// the Job without another StageAPK. Other errors continue.
func tryVersions(versions []string, fn func(ver string) error) error {
	if len(versions) == 0 {
		versions = []string{""}
	}
	var lastErr error
	for _, ver := range versions {
		err := fn(ver)
		if err == nil {
			return nil
		}
		lastErr = err
		if errors.Is(err, errStopWalk) {
			break
		}
	}
	if lastErr == nil {
		lastErr = fmt.Errorf("no versions to try: %w", ErrBase)
	}
	return lastErr
}

func (a *App) processVersion(ctx context.Context, job revanced.Job, ver string, reg download.Registry, order []string) error {
	log := logging.GetLogger(ctx)
	// Request label for downloaders (empty = source "latest" / newest release).
	job.PackageID = download.CanonicalPackage(job.PackageID)
	reqVer := ver
	stockPath := a.WS.StockAPKPath(job.PackageID, reqVer)
	var res *download.Result

	if workspace.CacheHit(stockPath) {
		if err := download.AcceptCached(stockPath); err != nil {
			log.Warn("stock cache rejected", "path", stockPath, "err", err)
		} else {
			log.Info("stock cache hit", "path", stockPath)
			res = &download.Result{Path: stockPath, SourceID: "cache"}
		}
	}
	if res == nil {
		label := reqVer
		if label == "" {
			label = "latest"
		}
		log.Info("download attempt", "package", job.PackageID, "version", label)
		got, err := download.FetchFirst(netx.WithLabel(ctx, "download stock "+job.PackageID), reg, order, download.Request{
			PackageID: job.PackageID,
			Version:   reqVer,
		}, a.WS.StockAPKs)
		if err != nil {
			return err
		}
		res = got
	}

	info, err := requireStockIdentity(res.Path, job.PackageID, reqVer)
	if err != nil {
		log.Warn("stock identity rejected", "path", res.Path, "source", res.SourceID, "err", err)
		osx.Remove(res.Path)
		if res.SourceID != "cache" {
			return err
		}
		label := emptyAsLatest(reqVer)
		log.Info("download attempt", "package", job.PackageID, "version", label)
		got, err := download.FetchFirst(netx.WithLabel(ctx, "download stock "+job.PackageID), reg, order, download.Request{
			PackageID: job.PackageID,
			Version:   reqVer,
		}, a.WS.StockAPKs)
		if err != nil {
			return err
		}
		res = got
		info, err = requireStockIdentity(res.Path, job.PackageID, reqVer)
		if err != nil {
			osx.Remove(res.Path)
			return err
		}
	}

	// Ground truth: versionName from the APK. "Any"/latest downloads must not
	// stay labeled "latest" in the F-Droid tree.
	resolved := reqVer
	if info.VersionName != "" {
		resolved = info.VersionName
	} else if info.VersionCode != "" {
		resolved = info.VersionCode
	} else if resolved == "" {
		resolved = "latest"
	}
	log.Info("resolved apk version", "package", job.PackageID, "versionName", info.VersionName, "versionCode", info.VersionCode)

	// Canonical stock cache path under the real version name.
	canonStock := a.WS.StockAPKPath(job.PackageID, resolved)
	if res.Path != canonStock {
		if err := moveFile(res.Path, canonStock); err != nil {
			return fmt.Errorf("canonicalize stock apk: %w", err)
		}
		res.Path = canonStock
	}

	outName := fmt.Sprintf("%s_%s_revanced.apk", strutil.Sanitize(job.PackageID), strutil.Sanitize(resolved))
	outPath := filepath.Join(a.WS.Work, outName)
	if err := os.MkdirAll(a.WS.Work, 0o755); err != nil {
		return err
	}

	var patches []string
	err = taskgroup.GoIsolated(ctx, "patch "+job.PackageID, taskgroup.CPU, func(ctx context.Context, s *taskgroup.Status) error {
		defer s.Unit()()
		s.Update("ReVanced CLI")
		log.Info("patching", "in", res.Path, "out", outPath, "version", resolved)
		ps, err := revanced.Patch(revanced.PatchOptions{
			CLIJar:                  a.WS.PatcherJAR(),
			PatchesRVP:              a.WS.PatchesRVP(),
			InputAPK:                res.Path,
			OutputAPK:               outPath,
			KeystorePath:            a.WS.KeystorePath,
			Blob:                    a.Blob,
			EnableChangePackageName: true,
		})
		if err != nil {
			return err
		}
		patches = ps
		return nil
	})
	if err != nil {
		return err
	}

	// INV-04: patched id must be stock + ".revanced" or this version is a miss.
	if err := requirePatchedIdentity(outPath, job.PackageID); err != nil {
		return err
	}

	pubID := job.PackageID + ".revanced"
	if err := taskgroup.GoIsolated(ctx, "stage "+job.PackageID, taskgroup.IO, func(ctx context.Context, s *taskgroup.Status) error {
		defer s.Unit()()
		s.Update("copy into F-Droid stage")
		return a.stagePatched(outPath, pubID, patches)
	}); err != nil {
		return err
	}
	log.Info("package ok", "package", job.PackageID, "version", resolved, "apk", outPath)
	return nil
}

// CheckStockIdentity is the TEC-03 identity gate: stock id match, version prefix.
func CheckStockIdentity(path, packageID, version string) error {
	_, err := requireStockIdentity(path, packageID, version)
	return err
}

func requireStockIdentity(path, packageID, version string) (apkmeta.Info, error) {
	info, err := apkmeta.Inspect(path)
	if err != nil {
		return apkmeta.Info{}, fmt.Errorf("apk identity: %w", err)
	}
	if err := info.MatchesRequest(packageID, version); err != nil {
		return apkmeta.Info{}, err
	}
	return info, nil
}

func requirePatchedIdentity(path, stockID string) error {
	info, err := apkmeta.Inspect(path)
	if err != nil {
		return fmt.Errorf("patched apk identity: %w", err)
	}
	return requirePatchedPackageID(info.PackageID, stockID)
}

func requirePatchedPackageID(got, stockID string) error {
	want := stockID + ".revanced"
	if got != want {
		return fmt.Errorf("patched package %q != %q: %w", got, want, ErrBase)
	}
	return nil
}

// stagePatched copies the APK then writes metadata. Metadata failure removes
// the staged APK and returns errStopWalk so the Job does not stage a second APK.
func (a *App) stagePatched(apkPath, pubID string, patches []string) error {
	if err := fdroid.StageAPK(a.WS.Stage, apkPath); err != nil {
		return err
	}
	if err := fdroid.WritePatchesMetadata(a.WS.Stage, pubID, patches); err != nil {
		osx.Remove(filepath.Join(a.WS.Stage, "repo", filepath.Base(apkPath)))
		return fmt.Errorf("patches metadata: %w: %w", err, errStopWalk)
	}
	return nil
}

func moveFile(src, dst string) error {
	if src == dst {
		return nil
	}
	if err := os.MkdirAll(filepath.Dir(dst), 0o755); err != nil {
		return err
	}
	if err := os.Rename(src, dst); err == nil {
		return nil
	}
	b, err := os.ReadFile(src)
	if err != nil {
		return err
	}
	if err := os.WriteFile(dst, b, 0o644); err != nil {
		return err
	}
	osx.Remove(src)
	return nil
}

// FDroidUpdate runs fdroid update on the CACHE stage tree (not live REPO).
func (a *App) FDroidUpdate(ctx context.Context, createMeta bool) error {
	if a.Blob == nil {
		return fmt.Errorf("signing not loaded: %w", ErrBase)
	}
	return taskgroup.GoIsolated(ctx, "rebuild F-Droid index", taskgroup.IO, func(ctx context.Context, s *taskgroup.Status) error {
		defer s.Unit()()
		s.Update("fdroid update")
		return fdroid.Update(a.WS.Stage, a.Blob, createMeta, a.WS.Shims)
	})
}

// pkgOutcome is a pure reduce element from the packages Map (no shared mutex).
type pkgOutcome struct {
	Package string
	OK      bool
	Skip    string // non-empty when soft-skipped
}

// Test hooks. Production values call the App methods / toolscheck.
var (
	checkTools     = func() error { return toolscheck.Check(toolscheck.DefaultRun()) }
	loadSigning    = (*App).LoadSigning
	prepareStage   = (*App).WriteFDroidConfig
	fetchTools     = (*App).FetchTools
	listJobs       = (*App).ListJobs
	processPackage = (*App).ProcessPackage
	fdroidUpdate   = (*App).FDroidUpdate
	publishStage   = (*App).PublishStage
)

// RunFull is the kitchen-sink pipeline for REPO.
func (a *App) RunFull(ctx context.Context) error {
	if err := checkTools(); err != nil {
		return err
	}
	log := logging.GetLogger(ctx)
	if err := loadSigning(a); err != nil {
		return err
	}
	if err := prepareStage(a); err != nil {
		return err
	}
	if err := fetchTools(a, ctx); err != nil {
		return err
	}

	jobs, err := listJobs(a)
	if err != nil {
		return err
	}
	// Shuffle so rate limits (403/429) and early aborts don't always starve the
	// same packages; over many runs every app gets a fair shot at updates.
	rand.Shuffle(len(jobs), func(i, j int) {
		jobs[i], jobs[j] = jobs[j], jobs[i]
	})
	log.Info("jobs loaded", "count", len(jobs), "repo", a.WS.Repo, "cache", a.WS.Cache)

	// One aggregate bar for all package APK work. Pure reduce after soft-skips.
	outcomes, err := taskgroup.Map[revanced.Job, pkgOutcome]{
		Name:     "packages",
		Items:    jobs,
		PoolKind: taskgroup.Control,
		TaskName: func(_ int, j revanced.Job) string { return j.PackageID },
		Fn: func(ctx context.Context, s *taskgroup.Status, job revanced.Job) (pkgOutcome, error) {
			s.Update("process " + job.PackageID)
			err := taskgroup.Isolate(ctx, func(ctx context.Context) error {
				return processPackage(a, ctx, job)
			})
			if err != nil {
				logging.GetLogger(ctx).Warn("skip package", "package", job.PackageID, "err", err)
				return pkgOutcome{Package: job.PackageID, Skip: err.Error()}, nil
			}
			return pkgOutcome{Package: job.PackageID, OK: true}, nil
		},
	}.Run(ctx)
	if err != nil {
		return err
	}

	var okPkgs, skipPkgs []string
	for _, o := range outcomes {
		if o.OK {
			okPkgs = append(okPkgs, o.Package)
			continue
		}
		if o.Skip != "" {
			skipPkgs = append(skipPkgs, o.Package+": "+o.Skip)
		}
	}

	summarize := func() {
		log.Info("run summary",
			"ok", len(okPkgs),
			"skipped", len(skipPkgs),
			"ok_packages", strings.Join(okPkgs, ","),
		)
		for _, line := range skipPkgs {
			log.Info("skipped", "detail", line)
		}
	}
	if s := taskgroup.SessionFrom(ctx); s != nil {
		s.AfterWait(func() error {
			summarize()
			return nil
		})
	} else {
		summarize()
	}

	log.Info("running fdroid update", "stage", a.WS.Stage)
	if err := fdroidUpdate(a, ctx, true); err != nil {
		return err
	}
	log.Info("publishing stage to REPO", "stage", a.WS.Stage, "repo", a.WS.Repo)
	if err := publishStage(a); err != nil {
		return err
	}
	log.Info("done", "repo", a.WS.Repo)
	return nil
}

// RunSmoke tries packages until maxOK succeed (or list exhausted). For TMP e2e.
// Job set is the same as run (TEC-04): every ListJobs entry, no package filter.
func (a *App) RunSmoke(ctx context.Context, maxOK int) (ok int, err error) {
	if err := checkTools(); err != nil {
		return 0, err
	}
	if err := loadSigning(a); err != nil {
		return 0, err
	}
	if err := prepareStage(a); err != nil {
		return 0, err
	}
	if err := fetchTools(a, ctx); err != nil {
		return 0, err
	}
	jobs, err := listJobs(a)
	if err != nil {
		return 0, err
	}
	return runSmoke(ctx, smokeRun{
		jobs:  jobs,
		maxOK: maxOK,
		try: func(ctx context.Context, job revanced.Job) error {
			return processPackage(a, ctx, job)
		},
		update:  func(ctx context.Context) error { return fdroidUpdate(a, ctx, true) },
		publish: func() error { return publishStage(a) },
	})
}

type smokeRun struct {
	jobs    []revanced.Job
	maxOK   int
	try     func(context.Context, revanced.Job) error
	update  func(context.Context) error
	publish func() error
}

func runSmoke(ctx context.Context, s smokeRun) (int, error) {
	log := logging.GetLogger(ctx)
	maxOK := s.maxOK
	if maxOK <= 0 {
		maxOK = 1
	}
	jobs := s.jobs
	// Shuffle so each smoke run picks a different starting app, then Serial
	// Each until maxOK succeed (stop scheduling via atomic).
	rand.Shuffle(len(jobs), func(i, j int) {
		jobs[i], jobs[j] = jobs[j], jobs[i]
	})
	if len(jobs) > 0 {
		log.Info("smoke order", "first", jobs[0].PackageID, "candidates", len(jobs), "max_ok", maxOK)
	}

	var okCount atomic.Int64
	err := taskgroup.Each[revanced.Job]{
		Name:     "smoke packages",
		Items:    jobs,
		PoolKind: taskgroup.Control,
		Serial:   true,
		TaskName: func(_ int, j revanced.Job) string { return j.PackageID },
		Fn: func(ctx context.Context, st *taskgroup.Status, job revanced.Job) error {
			if okCount.Load() >= int64(maxOK) {
				return nil
			}
			st.Update(job.PackageID)
			log.Info("smoke try", "package", job.PackageID)
			err := taskgroup.Isolate(ctx, func(ctx context.Context) error {
				return s.try(ctx, job)
			})
			if err != nil {
				log.Warn("smoke skip", "package", job.PackageID, "err", err)
				return nil
			}
			okCount.Add(1)
			return nil
		},
	}.Run(ctx)
	if err != nil {
		return 0, err
	}
	ok := int(okCount.Load())
	if ok == 0 {
		return 0, fmt.Errorf("no package succeeded download+patch (tried %d jobs): %w", len(jobs), ErrBase)
	}
	if err := s.update(ctx); err != nil {
		return ok, err
	}
	if err := s.publish(); err != nil {
		return ok, err
	}
	return ok, nil
}

func emptyAsLatest(v string) string {
	if v == "" {
		return "latest"
	}
	return v
}

func (a *App) stockRegistry() download.Registry {
	return download.DefaultRegistry(a.Cfg.BrowserCDPURL)
}
