// Command lab runs the market-connection study and writes reports/<world>.json.
//
//	go run ./cmd/lab                       # the development world
//	go run ./cmd/lab -lock                 # pin the held-out files (once, committed)
//	go run ./cmd/lab -world heldout        # the held-out world: refuses if a pinned file changed or it already ran
package main

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"flag"
	"fmt"
	"log"
	"os"
	"strings"
	"time"

	"github.com/manojrevenkar12-web/market-visibility-platform/internal/experiment"
	"github.com/manojrevenkar12-web/market-visibility-platform/internal/market"
)

var pinned = []string{"config/world_heldout.json", "config/lab.json"}

const lockFile = "config/heldout.lock"

func digest(path string) string {
	b, err := os.ReadFile(path)
	if err != nil {
		log.Fatal(err)
	}
	s := sha256.Sum256(b)
	return hex.EncodeToString(s[:])
}

func checkLock(out string) error {
	if _, err := os.Stat(out); err == nil {
		return fmt.Errorf("%s exists: the held-out world has had its one run", out)
	}
	b, err := os.ReadFile(lockFile)
	if err != nil {
		return fmt.Errorf("no %s: pin the files with -lock first", lockFile)
	}
	for _, line := range strings.Split(strings.TrimSpace(string(b)), "\n") {
		f := strings.Fields(line)
		if len(f) != 2 || digest(f[1]) != f[0] {
			return fmt.Errorf("%s changed since the lock was written; the held-out run would not be blind", f[len(f)-1])
		}
	}
	return nil
}

func main() {
	world := flag.String("world", "dev", "dev or heldout")
	lock := flag.Bool("lock", false, "pin the held-out files and exit")
	seeds := flag.Int("seeds", 0, "override the number of replications (development only)")
	flag.Parse()
	if *lock {
		var b strings.Builder
		for _, p := range pinned {
			fmt.Fprintf(&b, "%s  %s\n", digest(p), p)
		}
		if err := os.WriteFile(lockFile, []byte(b.String()), 0o644); err != nil {
			log.Fatal(err)
		}
		fmt.Println("pinned", strings.Join(pinned, ", "))
		return
	}
	cfgPath, out := "config/world.json", "reports/dev.json"
	if *world == "heldout" {
		cfgPath, out = "config/world_heldout.json", "reports/heldout.json"
		if err := checkLock(out); err != nil {
			log.Fatal(err)
		}
		if *seeds != 0 {
			log.Fatal("the held-out world runs with the pinned settings")
		}
	}
	cfg, err := market.LoadConfig(cfgPath)
	if err != nil {
		log.Fatal(err)
	}
	lab, err := experiment.LoadLab("config/lab.json")
	if err != nil {
		log.Fatal(err)
	}
	if *seeds > 0 {
		lab.Seeds = *seeds
	}
	start := time.Now()
	runs := experiment.RunAll(cfg, lab)
	rep := experiment.Summarize(cfg, lab, runs)
	b, _ := json.MarshalIndent(map[string]any{"report": rep, "seconds": time.Since(start).Round(time.Second).Seconds()}, "", " ")
	if err := os.WriteFile(out, append(b, '\n'), 0o644); err != nil {
		log.Fatal(err)
	}
	fmt.Printf("wrote %s in %s\n", out, time.Since(start).Round(time.Second))
}
