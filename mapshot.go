package main

import (
	"archive/zip"
	"context"
	goflag "flag"
	"fmt"
	"io/ioutil"
	"os"
	"path"
	"strings"
	"time"

	"github.com/Palats/mapshot/embed"
	"github.com/Palats/mapshot/factorio"
	"github.com/golang/glog"
	"github.com/google/uuid"
	"github.com/otiai10/copy"
	"github.com/spf13/cobra"
	flag "github.com/spf13/pflag"
)

var factorioSettings = factorio.RegisterFlags(goflag.CommandLine, "factorio_")

var rootCmd = &cobra.Command{
	Use:   "mapshot",
	Short: "mapshot generates zoomable screenshot for Factorio",
	// Do not show help if not requested - e.g., when an error is generated.
	SilenceUsage: true,
}

var cmdVersion = &cobra.Command{
	Use:   "version",
	Short: "Show the version of the mod.",
	Args:  cobra.NoArgs,
	Run: func(cmd *cobra.Command, args []string) {
		fmt.Println(embed.Version)
	},
}

var cmdPackage = &cobra.Command{
	Use:   "package",
	Short: "Generates the zip file of the Factorio mod.",
	Args:  cobra.NoArgs,
	RunE: func(cmd *cobra.Command, args []string) error {
		name := fmt.Sprintf("mapshot_%s", embed.Version)
		zipfilename := name + ".zip"
		zipfile, err := os.Create(zipfilename)
		if err != nil {
			return fmt.Errorf("unable to open file %s for creation: %w", zipfilename, err)
		}
		defer zipfile.Close()

		w := zip.NewWriter(zipfile)
		for filename, content := range embed.ModFiles {
			f, err := w.Create(path.Join(name, filename))
			if err != nil {
				return fmt.Errorf("unable to add %q to zip file: %w", filename, err)
			}
			if _, err = f.Write([]byte(content)); err != nil {
				return fmt.Errorf("unable to write %q to zip file: %w", filename, err)
			}
		}
		if err := w.Close(); err != nil {
			return fmt.Errorf("unable to close zipfile %s: %w", zipfilename, err)
		}
		return nil
	},
}

var cmdInfo = &cobra.Command{
	Use:   "info",
	Short: "Show info about what mapshot knows.",
	Args:  cobra.NoArgs,
	RunE: func(cmd *cobra.Command, args []string) error {
		fact, err := factorio.New(factorioSettings)
		if err != nil {
			return err
		}
		fmt.Println("datadir:", fact.DataDir())
		fmt.Println("binary:", fact.Binary())
		return nil
	},
}

var cmdRender = &cobra.Command{
	Use:   "render",
	Short: "Create a screenshot from a save.",
	Args:  cobra.ExactArgs(1),
	RunE: func(cmd *cobra.Command, args []string) error {
		fact, err := factorio.New(factorioSettings)
		if err != nil {
			return err
		}

		runID := uuid.New().String()
		glog.Infof("runid: %s", runID)

		name := args[0]
		fmt.Printf("Generating mapshot for savegame %q\n", name)

		tmpdir, err := ioutil.TempDir("", "mapshot")
		if err != nil {
			return fmt.Errorf("unable to create temp dir: %w", err)
		}
		glog.Info("temp dir: ", tmpdir)

		// Copy game save
		srcSavegame := fact.SaveFile(name)
		dstSavegame := path.Join(tmpdir, name+".zip")
		if err := copy.Copy(srcSavegame, dstSavegame); err != nil {
			return fmt.Errorf("unable to copy file %q: %w", srcSavegame, err)
		}
		glog.Infof("copied save from %q to %q", srcSavegame, dstSavegame)

		// Copy mods
		srcMods := fact.ModsDir()
		dstMods := path.Join(tmpdir, "mods")
		dstMapshot := path.Join(dstMods, "mapshot")
		foundModList := false

		// Create the mod directory first, in case the first file we encounter
		// is the mod-list.json (otherwise, the copy mechanism would create what
		// is needed).
		if err := os.MkdirAll(dstMapshot, 0755); err != nil {
			return fmt.Errorf("unable to create dir %q: %w", dstMapshot, err)
		}

		subs, err := ioutil.ReadDir(srcMods)
		if err != nil {
			return fmt.Errorf("unable to read directory %q: %w", srcMods, err)
		}
		for _, sub := range subs {
			src := path.Join(srcMods, sub.Name())
			dst := path.Join(dstMods, sub.Name())

			// Do not include existing mapshot plugin - it will be added afterward explictly.
			if sub.Name() == "mapshot" || strings.HasPrefix(sub.Name(), "mapshot_") {
				glog.Infof("ignoring mod file %q", src)
				continue
			}
			// Fiddle with the mod list to activate mapshot automatically.
			if sub.Name() == "mod-list.json" {
				mlist, err := factorio.LoadModList(src)
				if err != nil {
					return err
				}
				mlist.Enable("mapshot")

				if err := mlist.Write(dst); err != nil {
					return err
				}
				glog.Infof("created mod-list.json")
				foundModList = true
				continue
			}

			// Other mods and file, just copy.
			err = copy.Copy(src, dst, copy.Options{OnSymlink: func(string) copy.SymlinkAction { return copy.Deep }})
			if err != nil {
				return fmt.Errorf("unable to copy %q to %q: %w", src, dst, err)
			}
			glog.Infof("copied mod file %q to %q", src, dst)
		}

		if !foundModList {
			return fmt.Errorf("unable to find `mod-list.json` in %q", srcMods)
		}
		glog.Infof("copied mods from %q to %q", srcMods, dstMods)

		// Add the mod itself.
		for name, content := range embed.ModFiles {
			dst := path.Join(dstMapshot, name)
			if err := ioutil.WriteFile(dst, []byte(content), 0644); err != nil {
				return fmt.Errorf("unable to write file %q: %w", dst, err)
			}
		}
		glog.Infof("mod created at %q", dstMapshot)

		overrides := fmt.Sprintf(`
		return {
			onstartup = "%s",
			shotname = "%s",
			tilemin = 64,
		}
		`, runID, name)
		overridesFilename := path.Join(dstMapshot, "overrides.lua")
		if err := ioutil.WriteFile(overridesFilename, []byte(overrides), 0644); err != nil {
			return fmt.Errorf("unable to write overrides file %q: %w", overridesFilename, err)
		}
		glog.Infof("overrides file created at %q", overridesFilename)

		// Remove done marker if still present
		doneFile := path.Join(fact.ScriptOutput(), "mapshot-done-"+runID)
		err = os.Remove(doneFile)
		glog.Infof("removed done-file %q: %v", doneFile, err)

		factorioArgs := []string{
			"--disable-audio",
			"--disable-prototype-history",
			"--load-game", dstSavegame,
			"--mod-directory", dstMods,
			"--force-graphics-preset", "very-low",
		}
		glog.Infof("Factorio args: %v", args)

		ctx := cmd.Context()
		cancel := func() {}
		if !keepRunning {
			ctx, cancel = context.WithCancel(ctx)
			defer cancel()
		}
		errCh := make(chan error)
		fmt.Println("Starting Factorio...")
		go func() {
			errCh <- fact.Run(ctx, factorioArgs)
		}()

		// Wait for the `done` file to be created, indicating that the work is
		// done.
		for {
			_, err := os.Stat(doneFile)
			if err != nil && !os.IsNotExist(err) {
				return fmt.Errorf("unable to stat file %q: %w", doneFile, err)
			}
			if err == nil {
				cancel()
				break
			}

			// Context cancellation should terminate Factorio, which is detected
			// through errCh, so no need to wait on context.
			select {
			case <-time.After(time.Second):
			case err := <-errCh:
				return fmt.Errorf("factorio exited early: %w", err)
			}
		}
		glog.Infof("done file %q now exists", doneFile)
		rawDone, err := ioutil.ReadFile(doneFile)
		if err != nil {
			return fmt.Errorf("unable to read file %q: %w", doneFile, err)
		}
		resultPrefix := string(rawDone)
		glog.Infof("output at %s", resultPrefix)

		err = <-errCh
		if err != nil && err.Error() != "signal: killed" {
			return fmt.Errorf("error while running Factorio: %w", err)
		}

		// Remove temporary directory.
		if err := os.RemoveAll(tmpdir); err != nil {
			return fmt.Errorf("unable to remove temp dir %q: %w", tmpdir, err)
		}
		glog.Infof("temp dir %q removed", tmpdir)

		fmt.Println("Output:", path.Join(fact.ScriptOutput(), resultPrefix))
		return nil
	},
}

var keepRunning bool

func main() {
	flag.CommandLine.AddGoFlagSet(goflag.CommandLine)
	cmdRender.PersistentFlags().BoolVar(&keepRunning, "keep_running", false, "If true, wait for Factorio to exit instead of stopping it.")

	rootCmd.AddCommand(cmdPackage)
	rootCmd.AddCommand(cmdVersion)
	rootCmd.AddCommand(cmdInfo)
	rootCmd.AddCommand(cmdRender)

	// Fake parse the default Go flags - that appease glog, which otherwise
	// complains on each line. goflag.CommandLine do get parsed in parsed
	// through pflag and `AddGoFlagSet`.
	goflag.CommandLine.Parse(nil)

	if err := rootCmd.ExecuteContext(context.Background()); err != nil {
		// Root cmd already prints errors of subcommands.
		os.Exit(1)
	}

}