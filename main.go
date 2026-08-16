// backlogr: log into Steam, show the first unplayed game in your library, alphabetically.
package main

import (
	"context"
	"encoding/json"
	"flag"
	"fmt"
	"html"
	"io"
	"log"
	"maps"
	"net/http"
	"net/url"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"runtime"
	"sort"
	"strings"
	"time"
	"unicode"

	"github.com/charmbracelet/lipgloss"
)

const (
	listenAddr  = "127.0.0.1:8420"
	steamOpenID = "https://steamcommunity.com/openid/login"

	loggedInHTML = `<body style="background:#1c1917;color:#d7d7d7;font:16px/1.6 ui-monospace,monospace;` +
		`display:grid;place-items:center;height:100vh;margin:0">` +
		`<div><h2 style="color:#ff5fd7;margin:0">Logged in.</h2>` +
		`<p style="color:#8a8a8a">Back to your terminal.</p></div></body>`
)

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

// ~/.config/backlogr/steamid, or under $XDG_CONFIG_HOME if set
func steamIDPath() (string, error) {
	dir := os.Getenv("XDG_CONFIG_HOME")
	if dir == "" {
		home, err := os.UserHomeDir()
		if err != nil {
			return "", err
		}
		dir = filepath.Join(home, ".config")
	}
	return filepath.Join(dir, "backlogr", "steamid"), nil
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
	flag.Parse()

	loadDotEnv(".env")

	apiKey := os.Getenv("STEAM_API_KEY")
	if apiKey == "" {
		log.Fatal(ErrorStyle.Render("set STEAM_API_KEY (get one at https://steamcommunity.com/dev/apikey)"))
	}

	if !*relogin {
		if id := cachedSteamID(); id != "" {
			if err := report(apiKey, id); err != nil {
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
		if err := report(apiKey, steamID); err != nil {
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

func report(apiKey, steamID string) error {
	games, err := ownedGames(apiKey, steamID)
	if err != nil {
		return err
	}

	var unplayed []game
	for _, g := range games {
		if g.Playtime == 0 {
			unplayed = append(unplayed, g)
		}
	}
	if len(unplayed) == 0 {
		fmt.Println(ViewStyle.Render(TitleStyle.Render(
			fmt.Sprintf("No unplayed games in %d owned. Impressive.", len(games)))))
		return nil
	}
	sort.Slice(unplayed, func(i, j int) bool {
		a, b := sortKey(unplayed[i].Name), sortKey(unplayed[j].Name)
		if a != b {
			return a < b
		}
		return unplayed[i].Name < unplayed[j].Name
	})

	g := unplayed[0]
	desc, err := shortDescription(g.AppID)
	if err != nil {
		desc = "(no description available)"
	}

	meta := MetaStyle.Render(fmt.Sprintf("0 hours played · app %d · ", g.AppID)) +
		CountStyle.Render(fmt.Sprint(len(unplayed))) +
		MetaStyle.Render(fmt.Sprintf(" unplayed of %d owned", len(games)))

	fmt.Println(ViewStyle.Render(lipgloss.JoinVertical(lipgloss.Left,
		TitleStyle.Render(g.Name),
		meta,
		DescStyle.Render(desc),
	)))
	return nil
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
