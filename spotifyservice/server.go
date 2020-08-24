// Copyright 2020 Sergio Chairez. All rights reserved.
// Use of this source code is governed by a MIT style license that can be found
// in the LICENSE file.

package spotifyservice

import (
	"context"
	"crypto/rand"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"log"
	"net/http"
	"os"
	"path/filepath"
	"time"

	"github.com/go-chi/chi"
	"github.com/go-chi/chi/middleware"
	"github.com/go-chi/cors"
	"github.com/schairez/spotifywork/env"
	"github.com/schairez/spotifywork/internal"
	"github.com/schairez/spotifywork/spotifyservice/spotifyapi"
)

/*

 rt http.RoundTripper,

 https://chromium.googlesource.com/external/github.com/golang/oauth2/+/8f816d62a2652f705144857bbbcc26f2c166af9e/oauth2.go
*/

const stateCookieName = "oauthState"

func genRandState() string {
	log.Println("generating rand bytes")
	bytes := make([]byte, 32)
	// rand.Read(b)
	if _, err := rand.Read(bytes); err != nil {
		log.Fatalf("failed to read rand fn %v", err)
	}
	state := base64.StdEncoding.EncodeToString(bytes)
	return state
}

//Server is the component of our app
type Server struct {
	cfg        *env.TomlConfig
	client     *spotifyapi.Client
	router     *chi.Mux
	httpServer *http.Server
}

//NewServer returns a configured new spotify client server
func NewServer(fileName string) *Server {
	s := &Server{}
	s.initCfg(fileName)
	s.initClient()
	s.routes()
	return s
}

//TODO: make a filePathErr for initCfg

func (s *Server) initCfg(fileName string) {
	cfg, err := env.LoadTOMLFile(fileName)
	if err != nil {
		log.Fatal("Error loading .toml file into struct config")
	}
	s.cfg = cfg

}

func (s *Server) initClient() {
	cfg, ok := s.cfg.Oauth2Providers["spotify"]
	if !ok {
		// TODO: Properly handle error
		panic("Spotify env properties not found in config")
	}
	s.client = spotifyapi.NewClient(
		cfg.ClientID,
		cfg.ClientSecret,
		cfg.RedirectURL)
}

//routes inits the route multiplexer with the assigned routes
func (s *Server) routes() {
	s.router = chi.NewRouter()

	cors := cors.New(cors.Options{
		AllowedOrigins:   []string{"*"},
		AllowedMethods:   []string{"GET", "POST", "PUT", "DELETE", "OPTIONS"},
		AllowedHeaders:   []string{"Accept", "Authorization", "Content-Type", "X-CSRF-Token"},
		AllowCredentials: true,
		MaxAge:           300, // Maximum value not ignored by any of major browsers
	})
	s.router.Use(
		middleware.Logger,       // Log API request calls
		middleware.StripSlashes, // Strip slashes to no slash URL versions
		middleware.RealIP,
		middleware.Recoverer, // Recover from panics without crashing server
		cors.Handler,         // Enable CORS globally
	)
	// Index handler
	s.router.Get("/", func(w http.ResponseWriter, r *http.Request) {
		w.Write([]byte("hi"))
	})

	//test connection
	s.router.Get("/ping", s.handlePing) //GET /ping

	s.router.Get("/", s.handleHome)

	//serve static files
	workDir, _ := os.Getwd()
	filesDir := http.Dir(filepath.Join(workDir, "data"))
	FileServer(s.router, "/templates", filesDir)

	//account signin with Spotify
	// s.router.Get("/accounts/signup")

	s.router.Get("/auth", func(w http.ResponseWriter, r *http.Request) {
		ctx := r.Context()
		log.Println(ctx)
		//check if the request contains a cookie?
		//COOKIE would be attached if the use has hit our domain
		//this would indicate that a user-agent has hit this endpoint, but not
		//that the user has authorized our app per-say
		log.Println("checking if user already has a cookie stored in their browser")
		cookie, err := r.Cookie(stateCookieName)
		if err != nil {
			log.Printf("we got no cookie in request, %s", err)
		}
		fmt.Println(cookie)
		localState := genRandState()
		//setting the set-Cookie header in the writer
		//NOTE: headers need to be set before anything else set to the writer
		http.SetCookie(w, internal.NewCookie(stateCookieName, localState))
		fmt.Println(localState)
		fmt.Println(w.Header())
		authURL := s.client.Config.AuthCodeURL(localState)
		//app directs user-agent to spotify's oauth2 auth  consent page
		http.Redirect(w, r, authURL, http.StatusTemporaryRedirect)

	})
	//below we have our redirect callback as a result of a user-agent accessing
	//our /auth endpoint route
	s.router.Get("/auth/callback", func(w http.ResponseWriter, r *http.Request) {
		//check if user denied our auth request the request we receive
		//would contain a non-empty error query param in this case
		if r.FormValue("error") != "" {
			log.Printf("user authorization failed. Reason=%s", r.FormValue("error"))
			http.Redirect(w, r, "/", http.StatusTemporaryRedirect)
			return
		}
		//check the state parameter we supplied to Spotify's Account's Service earlier
		//if user approved the auth, we'll have both a code and a state query param
		oauthStateCookie, err := r.Cookie(stateCookieName)
		if err != nil {
			if err == http.ErrNoCookie {
				log.Println("Error finding cookie: ", err.Error())
				http.Redirect(w, r, "/", http.StatusUnauthorized)
			}
			http.Error(w, err.Error(), http.StatusBadRequest)
			return
		}
		log.Printf("%s=%s\r\n", oauthStateCookie.Name, oauthStateCookie.Value)
		if r.FormValue("state") != oauthStateCookie.Value {
			log.Println("invalid oauth2 spotify state. state_mismatch err")
			//http.Error(w, "state_mismatch err", http.StatusUnauthorized)
			http.Redirect(w, r, "/", http.StatusUnauthorized)

			return
		}
		//TODO: pkce opts?
		authCode := r.FormValue("code")
		log.Printf("code=%s", authCode)
		//TODO: diff b/w background and oauth2.NoContext
		ctx := context.Background()
		//exchange auth code with an access token
		token, err := s.client.Config.Exchange(ctx, authCode)

		if err != nil {
			log.Printf("error converting auth code into token; %s", err.Error())
			http.Error(w, err.Error(), http.StatusBadRequest)
			// http.Error(w, err.Error(), http.StatusInternalServerError)
			//TODO:
			// or StatusForbidden?
			return
		}
		//we'll use the token to access user's protected resources
		// by calling the Spotify Web API

		log.Println(token)
		log.Println("query params?")
		queryParams := r.URL.Query()
		log.Println(queryParams)
		if reqHeadersBytes, err := json.Marshal(r.Header); err != nil {
			log.Println("Could not Marshal Req Headers")
		} else {
			log.Println(string(reqHeadersBytes))
		}

		//now we can use this token to call Spotify APIs on behalf of the user
		//use the token to get an authenticated client
		//the underlying transport obtained using ctx?
		user, err := s.client.GetUserProfileRequest(context.Background(), token)
		if err != nil {
			http.Error(w, err.Error(), http.StatusBadRequest)
			return
		}
		log.Println("getting user")
		log.Printf("%+v\n", user)

		// data, _ := ioutil.ReadAll(resp.Body)
		// log.Println("Data calling user API: ", string(data))
		limit := 50
		offset := 0
		market := "us"
		params := spotifyapi.QParams{Limit: &limit, Offset: &offset, Market: &market}
		tracks, err := s.client.GetUserSavedTracks(context.Background(), token, &params)
		if err != nil {
			http.Error(w, err.Error(), http.StatusBadRequest)
			return
		}
		log.Println("getting user tracks")
		b, err := json.MarshalIndent(*tracks, "", "  ")
		if err != nil {
			fmt.Println(err)
		}
		fmt.Print(string(b))
		// log.Printf("%+v\n", tracks)

	})

	s.router.Get("/logout/{provider}", func(w http.ResponseWriter, r *http.Request) {

		w.Header().Set("Location", "/")
		w.WriteHeader(http.StatusTemporaryRedirect)
	})

}

/*
fn that takes a Spotify URI, parses it with strings lib

*/

//401 err when no token provided

/*
{
  "error": {
    "status": 401,
    "message": "No token provided"
  }
}

*/

/*

doc: https://developer.spotify.com/documentation/web-api/reference/library/get-users-saved-tracks/
Endpoint:
GET /v1/me/tracks
NOTE:
- we can receive up to 10,000  of user's liked tracks (limit user can save)
TODO:
limit max 50, min 1, default 20
0ffset 0
we care about the track.album.artists.name

t
*/

//credit to uber-go guide on verifying interface compliance at compile time
//https://github.com/uber-go/guide/blob/master/style.md#guidelines
//this statement will fail if *Server ever stops matching the http.Handler interface
var _ http.Handler = (*Server)(nil)

/*
A struct or object will be Handler if it has one method ServeHTTP which takes ResponseWriter and pointer to Request.
*/

func (s *Server) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	s.router.ServeHTTP(w, r)
}

//Start starts the server
func (s *Server) Start() {
	s.httpServer = &http.Server{
		Addr:         ":" + s.cfg.Server.Port,
		Handler:      s.router,
		ReadTimeout:  10 * time.Second,
		WriteTimeout: 10 * time.Second,
	}

	log.Printf("server listening on %s\n", s.cfg.Server.Port)
	if err := s.httpServer.ListenAndServe(); err != http.ErrServerClosed {
		log.Fatalf("ListenAndServe err: %s", err)

	} else {
		log.Println("Server closed!")
	}

}

//Shutdown the server
func (s *Server) Shutdown() {

}

/*
ex:
req, err := http.NewRequest("GET", makeUrl("/search"), nil)

func makeUrl(path string) string {
	return "https://api.spotify.com/v1" + path
}

func SpotifyAPIRequest() {

}


*/

/*

func writeJSONResponse(w http.ResponseWriter, status int, data []byte) {
	w.Header().Set("Content-Type", "application/json")
	w.Header().Set("Content-Length", strconv.Itoa(len(data)))
	w.Header().Set("Connection", "close")
	w.WriteHeader(status)
	w.Write(data)
}
*/

/*

func newAPIRequest() (string, error) {
	var response *http.Response

	req, err := http.NewRequest("POST", oc.oauthUrl, strings.NewReader(postBody))
	req.Header.Add("Content-Type", "application/x-www-form-urlencoded")

	if err != nil {
		return "", err
	}

	return "", nil

}

*/
