package main

import (
	"bufio"
	"context"
	b64 "encoding/base64"
	"encoding/json"
	"errors"
	"os"
	"os/user"
	"path/filepath"
	"regexp"
	"time"
	"fmt"

	"github.com/fatih/color"
	"github.com/gen2brain/beeep"
	"github.com/git-lfs/go-netrc/netrc"
	"github.com/theckman/yacspin"

	"github.com/go-rod/rod"
	"github.com/go-rod/rod/lib/input"
	"github.com/go-rod/rod/lib/launcher"
	"github.com/go-rod/rod/lib/proto"
)

// Time before MFA step times out
const MFA_TIMEOUT = 30

var cfg = yacspin.Config{
	Frequency:         100 * time.Millisecond,
	CharSet:           yacspin.CharSets[59],
	Suffix:            "AWS SSO Signing in: ",
	SuffixAutoColon:   false,
	Message:           "",
	StopCharacter:     "✓",
	StopFailCharacter: "✗",
	StopMessage:       "Logged in successfully",
	StopFailMessage:   "Log in failed",
	StopColors:        []string{"fgGreen"},
}

var spinner, _ = yacspin.New(cfg)

func main() {
	spinner.Start()

	// get sso url from stdin
	url := getURL()
	// start aws sso login
	ssoLogin(url)

	spinner.Stop()
	time.Sleep(1 * time.Second)
}

// returns sso url from stdin.
func getURL() string {
	spinner.Message("reading url from stdin")

	scanner := bufio.NewScanner(os.Stdin)
	url := ""
	for url == "" {
		scanner.Scan()
		t := scanner.Text()
		r, _ := regexp.Compile("^https.*user_code=([A-Z]{4}-?){2}")

		if r.MatchString(t) {
			url = t
		}
	}

	return url
}

// get credentials from ENV, fallback to .netcrc file
func getCredentials() (string, string) {
    username, exists_u := os.LookupEnv("SSO_USERNAME")
    passphrase, exists_p := os.LookupEnv("SSO_PASSWORD")
    if (exists_u && exists_p) {
	    spinner.Message("Using credentials from environment.")
	    return username, passphrase
    }else{
      return getCredentialsFromNetrc()
    }
}

// get aws credentials from netrc file
func getCredentialsFromNetrc() (string, string) {

	usr, _ := user.Current()
	spinner.Message(fmt.Sprintf("fetching credentials from %s/.netrc", usr.HomeDir) )
	f, err := netrc.ParseFile(filepath.Join(usr.HomeDir, ".netrc"))
	if err != nil {
		panic(".netrc file not found in HOME directory")
	}

	username := f.FindMachine("headless-sso", "").Login
	passphrase := f.FindMachine("headless-sso", "").Password

	return username, passphrase
}

// login with hardware MFA
func ssoLogin(url string) {
	username, passphrase := getCredentials()
	spinner.Message(color.MagentaString("init headless-browser \n"))
	spinner.Pause()
	curl := launcher.New().
		Headless(true).
		Devtools(false).
		MustLaunch()

	browser := rod.New().ControlURL(curl).Trace(false).MustConnect()
	loadCookies(*browser)
	defer browser.MustClose()

	err := rod.Try(func() {
		page := browser.MustPage(url)
		w := page.WaitRequestIdle(time.Second*time.Duration(5), []string{}, []string{})
		w()
		// authorize
		spinner.Unpause()
		spinner.Message("logging in")

		page.MustElementR("button", "Confirm and continue").MustClick()

		spinner.Message("waited")

		page.Race().ElementR("span", "Allow access").MustHandle(func(e *rod.Element) {
		}).Element("#awsui-input-0").MustHandle(func(e *rod.Element) {
			signIn(*page, username, passphrase)
			// mfa required step
			mfa(*page)
		}).MustDo()

		// allow request
		unauthorized := true
		for unauthorized {

			exists, _, _ := page.HasR("span", "Allow access")
			if exists {
				page.MustElementR("span", "Allow access").MustClick()
				unauthorized = false
			}

			time.Sleep(500 * time.Millisecond)
		}

		exists, _, _ := page.HasR("div", "Request approved")

		if exists {
			spinner.StopMessage("Request approved")
			saveCookies(*browser)
			browser.MustClose()
		}

	})

	if errors.Is(err, context.DeadlineExceeded) {
		panic("Timed out waiting for MFA")
	} else if err != nil {
		panic(err.Error())
	}
}

// executes aws sso signin step
func signIn(page rod.Page, username, passphrase string) {
	page.MustElement("#awsui-input-0").MustInput(username).MustPress(input.Enter)
	page.MustElement("#awsui-input-1").MustInput(passphrase).MustPress(input.Enter)
}

// TODO: allow user to enter MFA Code
func mfa(page rod.Page) {
	_ = beeep.Notify("headless-sso", "Touch U2F device to proceed with authenticating AWS SSO", "")
	_ = beeep.Beep(beeep.DefaultFreq, beeep.DefaultDuration)

	spinner.Message(color.YellowString("Touch U2F"))
}

// load cookies
func loadCookies(browser rod.Browser) {
	spinner.Message("loading cookies")
	dirname, err := os.UserHomeDir()
	if err != nil {
		error(err.Error())
	}

	data, _ := os.ReadFile(dirname + "/.headless-sso")
	sEnc, _ := b64.StdEncoding.DecodeString(string(data))
	var cookie *proto.NetworkCookie
	json.Unmarshal(sEnc, &cookie)

	if cookie != nil {
		browser.MustSetCookies(cookie)
	}
}

// save authn cookie
func saveCookies(browser rod.Browser) {
	dirname, err := os.UserHomeDir()
	if err != nil {
		error(err.Error())
	}

	cookies := (browser.MustGetCookies())

	for _, cookie := range cookies {
		if cookie.Name == "x-amz-sso_authn" {
			data, _ := json.Marshal(cookie)

			sEnc := b64.StdEncoding.EncodeToString([]byte(data))
			err = os.WriteFile(dirname+"/.headless-sso", []byte(sEnc), 0644)

			if err != nil {
				error("Failed to save x-amz-sso_authn cookie")
			}
			break
		}
	}
}

// print error message and exit
func panic(errorMsg string) {
	red := color.New(color.FgRed).SprintFunc()
	spinner.StopFailMessage(red("Login failed error - " + errorMsg))
	spinner.StopFail()
	os.Exit(1)
}

// print error message
func error(errorMsg string) {
	yellow := color.New(color.FgYellow).SprintFunc()
	spinner.Message("Warn: " + yellow(errorMsg))
}
