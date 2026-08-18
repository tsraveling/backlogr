// backlogr: log into Steam, pick an unplayed game from your library, and stick with it
// until you play it, skip it, or ask for the next one.
package main

import (
	"bufio"
	"context"
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"html"
	"io"
	"log"
	"maps"
	"math/rand/v2"
	"net/http"
	"net/url"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"runtime"
	"sort"
	"strconv"
	"strings"
	"time"
	"unicode"

	"github.com/charmbracelet/lipgloss"
	"github.com/charmbracelet/x/term"
)

const (
	listenAddr  = "127.0.0.1:8420"
	steamOpenID = "https://steamcommunity.com/openid/login"
	apiKeyURL   = "https://steamcommunity.com/dev/apikey"

	loggedInHTML = `<body style="background:#1c1917;color:#d7d7d7;font:16px/1.6 ui-monospace,monospace;` +
		`display:grid;place-items:center;height:100vh;margin:0">` +
		`<div><h2 style="color:#ff5fd7;margin:0">Logged in.</h2>` +
		`<p style="color:#8a8a8a">Back to your terminal.</p></div></body>`
)

var apiKeyRe = regexp.MustCompile(`^[0-9A-F]{32}$`)

var errNoInput = errors.New("no Steam API key saved; run backlogr in a terminal to set one up, " +
	"or set STEAM_API_KEY")

// env wins over .env, .env wins over the saved key; prompts if nothing is found
func steamAPIKey(reprompt bool) (string, error) {
	if !reprompt {
		if k := os.Getenv("STEAM_API_KEY"); k != "" {
			return k, nil
		}
		loadDotEnv(".env")
		if k := os.Getenv("STEAM_API_KEY"); k != "" {
			return k, nil
		}
		if k := savedAPIKey(); k != "" {
			return k, nil
		}
	}
	return promptAPIKey()
}

func savedAPIKey() string {
	path, err := configPath("apikey")
	if err != nil {
		return ""
	}
	data, err := os.ReadFile(path)
	if err != nil {
		return ""
	}
	k := strings.ToUpper(strings.TrimSpace(string(data)))
	if !apiKeyRe.MatchString(k) {
		return ""
	}
	return k
}

func saveAPIKey(k string) error {
	path, err := configPath("apikey")
	if err != nil {
		return err
	}
	if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
		return err
	}
	return os.WriteFile(path, []byte(k+"\n"), 0o600)
}

// cheapest call that distinguishes a good key from a bad one
func validateAPIKey(k string) error {
	resp, err := http.Get("https://api.steampowered.com/ISteamWebAPIUtil/GetSupportedAPIList/v1/?key=" + url.QueryEscape(k))
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	io.Copy(io.Discard, resp.Body)
	if resp.StatusCode != http.StatusOK {
		return fmt.Errorf("steam rejected that key (%s)", resp.Status)
	}
	return nil
}

// walks the user through minting their own key and saves it
func promptAPIKey() (string, error) {
	stat, err := os.Stdin.Stat()
	if err != nil || stat.Mode()&os.ModeCharDevice == 0 {
		return "", errNoInput
	}

	fmt.Println(ViewStyle.Render(lipgloss.JoinVertical(lipgloss.Left,
		TitleStyle.Render("backlogr needs a Steam Web API key"),
		HelpStyle.Render("It is free, personal, and takes a minute."),
		DescStyle.Render("Opening "+apiKeyURL+" — sign in, put anything in the domain "+
			"field (localhost is fine), and copy the key it gives you. It is stored at "+
			"~/.config/backlogr/apikey and never leaves your machine."),
	)))
	openBrowser(apiKeyURL)

	fmt.Print(TitleStyle.Render("key: "))
	line, err := bufio.NewReader(os.Stdin).ReadString('\n')
	if err != nil && (err != io.EOF || line == "") {
		return "", errNoInput
	}
	k := strings.ToUpper(strings.TrimSpace(line))
	if !apiKeyRe.MatchString(k) {
		return "", fmt.Errorf("that does not look like a Steam API key (expected 32 hex characters)")
	}
	if err := validateAPIKey(k); err != nil {
		return "", err
	}
	if err := saveAPIKey(k); err != nil {
		return "", err
	}
	fmt.Println(HelpStyle.Render("saved to ~/.config/backlogr/apikey"))
	return k, nil
}

// loads KEY=value lines from .env, without clobbering the real environment
func loadDotEnv(path string) {
	data, err := os.ReadFile(path)
	if err != nil {
		return
	}

	for raw := range strings.Lines(string(data)) {
		line := strings.TrimSpace(raw)
		if line == "" || strings.HasPrefix(line, "#") {
			continue
		}
		k, v, ok := strings.Cut(line, "=")
		if !ok {
			continue
		}
		k = strings.TrimSpace(strings.TrimPrefix(k, "export "))
		v = strings.Trim(strings.TrimSpace(v), `"'`)
		if _, set := os.LookupEnv(k); !set {
			os.Setenv(k, v)
		}
	}
}

// ~/.config/backlogr/<name>, or under $XDG_CONFIG_HOME if set
func configPath(name string) (string, error) {
	dir := os.Getenv("XDG_CONFIG_HOME")
	if dir == "" {
		home, err := os.UserHomeDir()
		if err != nil {
			return "", err
		}
		dir = filepath.Join(home, ".config")
	}
	return filepath.Join(dir, "backlogr", name), nil
}

func steamIDPath() (string, error) { return configPath("steamid") }

// app IDs the user has passed on, one per line
func loadSkips() map[int]bool {
	skips := map[int]bool{}
	path, err := configPath("skipped")
	if err != nil {
		return skips
	}
	data, err := os.ReadFile(path)
	if err != nil {
		return skips
	}
	for line := range strings.Lines(string(data)) {
		if id, err := strconv.Atoi(strings.TrimSpace(line)); err == nil {
			skips[id] = true
		}
	}
	return skips
}

func addSkip(appID int) error {
	path, err := configPath("skipped")
	if err != nil {
		return err
	}
	if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
		return err
	}
	f, err := os.OpenFile(path, os.O_APPEND|os.O_CREATE|os.O_WRONLY, 0o600)
	if err != nil {
		return err
	}
	defer f.Close()
	_, err = fmt.Fprintf(f, "%d\n", appID)
	return err
}

// the game we are currently pointing the player at, with the playtime it had
// when we picked it, so we can show hours played since
type current struct {
	AppID    int `json:"appid"`
	Baseline int `json:"baseline"`
}

func loadCurrent() *current {
	path, err := configPath("current")
	if err != nil {
		return nil
	}
	data, err := os.ReadFile(path)
	if err != nil {
		return nil
	}
	var c current
	if err := json.Unmarshal(data, &c); err != nil || c.AppID == 0 {
		return nil
	}
	return &c
}

func saveCurrent(c current) error {
	path, err := configPath("current")
	if err != nil {
		return err
	}
	if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
		return err
	}
	data, err := json.Marshal(c)
	if err != nil {
		return err
	}
	return os.WriteFile(path, append(data, '\n'), 0o600)
}

func clearCurrent() error {
	path, err := configPath("current")
	if err != nil {
		return err
	}
	if err := os.Remove(path); err != nil && !os.IsNotExist(err) {
		return err
	}
	return nil
}

func resetSkips() error {
	path, err := configPath("skipped")
	if err != nil {
		return err
	}
	if err := os.Remove(path); err != nil && !os.IsNotExist(err) {
		return err
	}
	return clearCurrent()
}

func cachedSteamID() string {
	path, err := steamIDPath()
	if err != nil {
		return ""
	}
	data, err := os.ReadFile(path)
	if err != nil {
		return ""
	}
	id := strings.TrimSpace(string(data))
	if !steamID64Re.MatchString(id) {
		return ""
	}
	return id
}

func cacheSteamID(id string) {
	path, err := steamIDPath()
	if err != nil {
		return
	}
	if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
		return
	}
	if err := os.WriteFile(path, []byte(id+"\n"), 0o600); err != nil {
		log.Printf("could not cache steamid: %v", err)
	}
}

var steamID64Re = regexp.MustCompile(`^\d{17}$`)

func main() {
	relogin := flag.Bool("login", false, "force a fresh Steam login, ignoring the cached SteamID")
	skip := flag.Bool("skip", false, "mark the current game as skipped and pick another")
	reset := flag.Bool("reset", false, "clear all skips")
	newKey := flag.Bool("key", false, "enter a new Steam API key, replacing the saved one")
	var sequential bool
	flag.BoolVar(&sequential, "sequential", false, "pick the next unplayed game alphabetically instead of at random")
	flag.BoolVar(&sequential, "s", false, "shorthand for --sequential")
	flag.Parse()

	if *reset {
		if err := resetSkips(); err != nil {
			log.Fatal(ErrorStyle.Render(err.Error()))
		}
		fmt.Println(HelpStyle.Render("skips cleared"))
		if !*skip {
			return
		}
	}

	apiKey, err := steamAPIKey(*newKey)
	if err != nil {
		log.Fatal(ErrorStyle.Render(err.Error()))
	}

	if !*relogin {
		if id := cachedSteamID(); id != "" {
			if err := report(apiKey, id, *skip, sequential); err != nil {
				log.Fatal(ErrorStyle.Render(err.Error()))
			}
			return
		}
	}

	returnTo := "http://" + listenAddr + "/callback"
	done := make(chan struct{})
	mux := http.NewServeMux()
	srv := &http.Server{Addr: listenAddr, Handler: mux}

	mux.HandleFunc("/", func(w http.ResponseWriter, r *http.Request) {
		http.Redirect(w, r, authURL(returnTo), http.StatusFound)
	})

	mux.HandleFunc("/callback", func(w http.ResponseWriter, r *http.Request) {
		defer close(done)
		steamID, err := verify(r.URL.Query())
		if err != nil {
			http.Error(w, "login failed: "+err.Error(), http.StatusBadRequest)
			log.Print(err)
			return
		}
		cacheSteamID(steamID)
		fmt.Fprint(w, loggedInHTML)
		if f, ok := w.(http.Flusher); ok {
			f.Flush()
		}
		if err := report(apiKey, steamID, *skip, sequential); err != nil {
			log.Print(err)
		}
	})

	go func() {
		if err := srv.ListenAndServe(); err != nil && err != http.ErrServerClosed {
			log.Fatal(err)
		}
	}()

	loginURL := "http://" + listenAddr + "/"
	fmt.Println(HelpStyle.Render("Opening Steam login: " + loginURL))
	openBrowser(loginURL)

	select {
	case <-done:
	case <-time.After(5 * time.Minute):
		log.Print("timed out waiting for login")
	}
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()
	srv.Shutdown(ctx)
}

func authURL(returnTo string) string {
	realm := "http://" + listenAddr
	q := url.Values{
		"openid.ns":         {"http://specs.openid.net/auth/2.0"},
		"openid.mode":       {"checkid_setup"},
		"openid.return_to":  {returnTo},
		"openid.realm":      {realm},
		"openid.identity":   {"http://specs.openid.net/auth/2.0/identifier_select"},
		"openid.claimed_id": {"http://specs.openid.net/auth/2.0/identifier_select"},
	}
	return steamOpenID + "?" + q.Encode()
}

var claimedIDRe = regexp.MustCompile(`^https?://steamcommunity\.com/openid/id/(\d+)$`)

// asks Steam to confirm the callback params it just signed, returns the SteamID64
func verify(q url.Values) (string, error) {
	form := url.Values{}
	maps.Copy(form, q)
	form.Set("openid.mode", "check_authentication")

	resp, err := http.PostForm(steamOpenID, form)
	if err != nil {
		return "", err
	}
	defer resp.Body.Close()
	body, err := io.ReadAll(resp.Body)
	if err != nil {
		return "", err
	}
	if !strings.Contains(string(body), "is_valid:true") {
		return "", fmt.Errorf("steam rejected the assertion")
	}

	m := claimedIDRe.FindStringSubmatch(q.Get("openid.claimed_id"))
	if m == nil {
		return "", fmt.Errorf("bad claimed_id %q", q.Get("openid.claimed_id"))
	}
	return m[1], nil
}

type game struct {
	AppID    int    `json:"appid"`
	Name     string `json:"name"`
	Playtime int    `json:"playtime_forever"`
}

// shows the current game and how long it has been played since we picked it.
// With nothing current (or after a skip) it picks one: at random by default,
// alphabetically first in sequential mode. Once the current game has any
// playtime, space draws the next one.
func report(apiKey, steamID string, skip, sequential bool) error {
	games, err := ownedGames(apiKey, steamID)
	if err != nil {
		return err
	}
	byID := map[int]game{}
	for _, g := range games {
		byID[g.AppID] = g
	}
	skips := loadSkips()

	cur := loadCurrent()
	if cur != nil {
		if _, ok := byID[cur.AppID]; !ok || skips[cur.AppID] {
			cur = nil
			clearCurrent()
		}
	}

	if skip {
		if cur == nil {
			fmt.Println(HelpStyle.Render("nothing to skip"))
		} else {
			if err := addSkip(cur.AppID); err != nil {
				return err
			}
			fmt.Println(HelpStyle.Render("skipped " + byID[cur.AppID].Name))
			skips[cur.AppID] = true
			cur = nil
			if err := clearCurrent(); err != nil {
				return err
			}
		}
	}

	for {
		if cur == nil {
			pool := unplayedPool(games, skips)
			if len(pool) == 0 {
				return noneLeft(len(games), len(skips))
			}
			pick := pool[rand.IntN(len(pool))]
			if sequential {
				pick = pool[0]
			}
			cur = &current{AppID: pick.AppID, Baseline: pick.Playtime}
			if err := saveCurrent(*cur); err != nil {
				return err
			}
		}

		g := byID[cur.AppID]
		played := g.Playtime - cur.Baseline
		if played < 0 {
			played = 0
		}
		render(g, played, len(unplayedPool(games, skips)), len(games), len(skips))

		if played == 0 || !waitForNext() {
			return nil
		}
		cur = nil
		if err := clearCurrent(); err != nil {
			return err
		}
	}
}

// every owned game with no playtime that has not been skipped, in library order
func unplayedPool(games []game, skips map[int]bool) []game {
	var pool []game
	for _, g := range games {
		if g.Playtime == 0 && !skips[g.AppID] {
			pool = append(pool, g)
		}
	}
	sort.Slice(pool, func(i, j int) bool {
		a, b := sortKey(pool[i].Name), sortKey(pool[j].Name)
		if a != b {
			return a < b
		}
		return pool[i].Name < pool[j].Name
	})
	return pool
}

func noneLeft(owned, skipped int) error {
	msg := fmt.Sprintf("No unplayed games left in %d owned. Impressive.", owned)
	if skipped > 0 {
		msg = fmt.Sprintf("Nothing left — %d skipped. Run with --reset to start over.", skipped)
	}
	fmt.Println(ViewStyle.Render(TitleStyle.Render(msg)))
	return clearCurrent()
}

func render(g game, played, unplayed, owned, skipped int) {
	desc, err := shortDescription(g.AppID)
	if err != nil {
		desc = "(no description available)"
	}

	meta := MetaStyle.Render(playedText(played)+fmt.Sprintf(" · app %d · ", g.AppID)) +
		CountStyle.Render(fmt.Sprint(unplayed)) +
		MetaStyle.Render(fmt.Sprintf(" unplayed of %d owned", owned))
	if skipped > 0 {
		meta += MetaStyle.Render(fmt.Sprintf(" · %d skipped", skipped))
	}

	fmt.Println(ViewStyle.Render(lipgloss.JoinVertical(lipgloss.Left,
		TitleStyle.Render(g.Name),
		meta,
		DescStyle.Render(desc),
	)))
	if played > 0 {
		fmt.Println(HelpStyle.Render("  space for next"))
	}
}

// playtime since we picked the game
func playedText(mins int) string {
	switch {
	case mins == 0:
		return "0 hours played"
	case mins < 60:
		return fmt.Sprintf("%d minutes played", mins)
	}
	return fmt.Sprintf("%.1f hours played", float64(mins)/60)
}

// true if the user asked for the next game; false on anything else, or when
// there is no terminal to read from
func waitForNext() bool {
	fd := os.Stdin.Fd()
	if !term.IsTerminal(fd) {
		return false
	}
	state, err := term.MakeRaw(fd)
	if err != nil {
		return false
	}
	defer term.Restore(fd, state)

	var buf [1]byte
	if _, err := os.Stdin.Read(buf[:]); err != nil {
		return false
	}
	return buf[0] == ' '
}

// mimics Steam's library ordering: case-insensitive, leading punctuation and
// a leading article ignored ("(the) Gnorp" → "gnorp", "The Witcher" → "witcher")
func sortKey(name string) string {
	s := strings.Map(func(r rune) rune {
		switch {
		case unicode.IsLetter(r) || unicode.IsDigit(r):
			return unicode.ToLower(r)
		case unicode.IsSpace(r):
			return ' '
		}
		return -1
	}, name)
	s = strings.TrimSpace(s)

	for _, a := range []string{"the ", "an ", "a "} {
		if rest, ok := strings.CutPrefix(s, a); ok {
			return strings.TrimSpace(rest)
		}
	}
	return s
}

func ownedGames(apiKey, steamID string) ([]game, error) {
	q := url.Values{
		"key":                       {apiKey},
		"steamid":                   {steamID},
		"include_appinfo":           {"1"},
		"include_played_free_games": {"1"},
	}
	resp, err := http.Get("https://api.steampowered.com/IPlayerService/GetOwnedGames/v1/?" + q.Encode())
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()

	var out struct {
		Response struct {
			Games []game `json:"games"`
		} `json:"response"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&out); err != nil {
		return nil, err
	}
	if len(out.Response.Games) == 0 {
		return nil, fmt.Errorf("no games returned — is the profile's game details set to public?")
	}
	return out.Response.Games, nil
}

var tagRe = regexp.MustCompile(`<[^>]*>`)

func shortDescription(appID int) (string, error) {
	resp, err := http.Get(fmt.Sprintf("https://store.steampowered.com/api/appdetails?appids=%d", appID))
	if err != nil {
		return "", err
	}
	defer resp.Body.Close()

	var out map[string]struct {
		Success bool `json:"success"`
		Data    struct {
			ShortDescription string `json:"short_description"`
		} `json:"data"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&out); err != nil {
		return "", err
	}
	entry, ok := out[fmt.Sprint(appID)]
	if !ok || !entry.Success || entry.Data.ShortDescription == "" {
		return "", fmt.Errorf("no store data for app %d", appID)
	}
	return html.UnescapeString(tagRe.ReplaceAllString(entry.Data.ShortDescription, "")), nil
}

func openBrowser(u string) {
	var cmd *exec.Cmd
	switch runtime.GOOS {
	case "darwin":
		cmd = exec.Command("open", u)
	case "windows":
		cmd = exec.Command("rundll32", "url.dll,FileProtocolHandler", u)
	default:
		cmd = exec.Command("xdg-open", u)
	}
	cmd.Start()
}
