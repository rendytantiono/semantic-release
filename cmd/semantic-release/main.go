package main

import (
	"errors"
	"fmt"
	"io/ioutil"
	"log"
	"os"
	"regexp"
	"strings"

	"github.com/go-semantic-release/semantic-release/pkg/condition"
	"github.com/go-semantic-release/semantic-release/pkg/config"
	"github.com/go-semantic-release/semantic-release/pkg/semrel"
	"github.com/go-semantic-release/semantic-release/pkg/update"
	"github.com/urfave/cli/v2"
)

// SRVERSION is the semantic-release version (added at compile time)
var SRVERSION string

func errorHandler(logger *log.Logger) func(error, ...int) {
	return func(err error, exitCode ...int) {
		if err != nil {
			logger.Println(err)
			if len(exitCode) == 1 {
				os.Exit(exitCode[0])
				return
			}
			os.Exit(1)
		}
	}
}

func main() {
	app := cli.NewApp()
	app.Name = "semantic-release"
	app.Usage = "automates the package release workflow including: determining the next version number and generating the change log"
	app.Version = SRVERSION
	app.Flags = config.CliFlags
	app.Action = cliHandler

	err := app.Run(os.Args)
	if err != nil {
		fmt.Printf("\n%s\n", err.Error())
		os.Exit(1)
	}
}

func cliHandler(c *cli.Context) error {

	logger := log.New(os.Stderr, "[semantic-release]: ", 0)
	exitIfError := errorHandler(logger)

	conf, err := config.NewConfig(c)
	exitIfError(err)

	ci := condition.NewCI()
	logger.Printf("detected CI: %s\n", ci.Name())

	var repo semrel.Repository

	if conf.GitLab {
		repo, err = semrel.NewGitLabRepository(c.Context, conf.GitLabBaseURL, conf.Slug, conf.Token, ci.GetCurrentBranch(), conf.GitLabProjectID)
	} else {
		repo, err = semrel.NewGitHubRepository(c.Context, conf.GheHost, conf.Slug, conf.Token)
	}

	logger.Printf("releasing on: %s\n", repo.Provider())

	exitIfError(err)

	logger.Println("getting default branch...")
	defaultBranch, isPrivate, err := repo.GetInfo()
	exitIfError(err)

	logger.Println("found default branch: " + defaultBranch)
	if isPrivate {
		logger.Println("repo is private")
	}

	currentBranch := ci.GetCurrentBranch()
	if conf.CurrentBranch != "" {
		currentBranch = conf.CurrentBranch
	}
	if currentBranch == "" {
		exitIfError(fmt.Errorf("current branch not found"))
	}
	logger.Println("found current branch: " + currentBranch)

	if conf.BetaRelease.MaintainedVersion != "" && currentBranch == defaultBranch {
		exitIfError(fmt.Errorf("maintained version not allowed on default branch"))
	}

	if conf.BetaRelease.MaintainedVersion != "" {
		logger.Println("found maintained version: " + conf.BetaRelease.MaintainedVersion)
		defaultBranch = "*"
	}

	currentSha := ci.GetCurrentSHA()
	if conf.CurrentBranch != "" {
		currentSha = conf.CurrentBranch
	}
	logger.Println("found current sha: " + currentSha)

	if !conf.Noci {
		logger.Println("running CI condition...")
		config := condition.CIConfig{
			"token":         conf.Token,
			"defaultBranch": defaultBranch,
			"private":       isPrivate || conf.TravisCom,
		}
		exitIfError(ci.RunCondition(config), 66)
	}

	logger.Println("getting latest release...")
	var matchRegex *regexp.Regexp
	match := strings.TrimSpace(conf.Match)
	if match != "" {
		logger.Printf("getting latest release matching %s...", match)
		matchRegex = regexp.MustCompile("^" + match)
	}
	lastVersionHotfix := ""
	if conf.CurrentBranch != conf.BaseBranch {
		lastVersionHotfix = strings.Trim(conf.CurrentBranch, conf.Pkgname+"-branch-v")
	}
	release, err := repo.GetLatestRelease(conf.BetaRelease.MaintainedVersion, matchRegex, conf.Pkgname, lastVersionHotfix)
	exitIfError(err)
	logger.Println("found version: " + release.Version.String())

	if strings.Contains(conf.BetaRelease.MaintainedVersion, "-") && release.Version.Prerelease() == "" {
		exitIfError(fmt.Errorf("no pre-release for this version possible"))
	}

	logger.Println("getting commits...")
	commits, err := repo.GetCommits(currentSha, conf.Pkgname)
	exitIfError(err)

	logger.Println("calculating new version...")
	newVer := semrel.GetNewVersion(conf, commits, release)
	if newVer == nil {
		if conf.CurrentBranch != conf.BaseBranch {
			newVerMinor := release.Version.IncPatch()
			newVer = &newVerMinor
		} else {
			newVerMinor := release.Version.IncMinor()
			log.Println(newVerMinor)
			newVer = &newVerMinor
		}
	}

	if conf.Dry {
		exitIfError(errors.New("DRY RUN: no release was created"), 65)
	}

	logger.Println("generating changelog...")
	changelog := semrel.GetChangelog(commits, release, newVer)
	if conf.Changelog != "" {
		exitIfError(ioutil.WriteFile(conf.Changelog, []byte(changelog), 0644))
	}

	SHA := ""
	foundCommit := false
	if len(commits) > 0 {
		SHA = commits[0].SHA
		for _, j := range commits {
			if j.SHA == conf.CommitHash {
				SHA = j.SHA
				foundCommit = true
			}
		}
	}
	if foundCommit == false {
		exitIfError(errors.New("Commit not found"), 65)
	}
	logger.Println("creating release...")
	exitIfError(repo.CreateRelease(changelog, newVer, conf.Prerelease, currentBranch, currentSha, SHA, conf.Pkgname))

	if conf.Ghr {
		exitIfError(ioutil.WriteFile(".ghr", []byte(fmt.Sprintf("-u %s -r %s v%s", repo.Owner(), repo.Repo(), newVer.String())), 0644))
	}

	if conf.Vf {
		exitIfError(ioutil.WriteFile(".version", []byte(newVer.String()), 0644))
	}

	if conf.Update != "" {
		exitIfError(update.Apply(conf.Update, newVer.String()))
	}

	logger.Println("done.")
	return nil
}
