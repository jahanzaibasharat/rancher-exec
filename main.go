package main

import (
	"encoding/base64"
	"encoding/json"
	"fmt"
	"io"
	"io/ioutil"
	"net/http"
	"os"
	"strings"

	"gopkg.in/alecthomas/kingpin.v2"

	"golang.org/x/crypto/ssh/terminal"

	"github.com/gorilla/websocket"
	"github.com/spf13/viper"
	"bufio"
	"strconv"
)

const (
	VERSION = "1.0.0"
	AUTHOR  = "MVLM <vladzikus@gmail.com>"
	USAGE   = `
Example:
    rancherexec my-server-1
    rancherexec "my-server*"  (equals to) rancherexec my-server%
    rancherexec %proxy%
    rancherexec "projectA-app-*" (equals to) rancherexec projectA-app-%

Configuration:
    We read configuration from config.json or config.yml in ./, /etc/rancherexec/ and ~/.rancherexec/ folders.

    If you want to use JSON format, create a config.json in the folders with content:
        {
            "url": "https://rancher.server/v1", // Or "https://rancher.server/v1/projects/xxxx"
            "access_key": "your_access_key",
            "secret_key": "your_access_secret_key"
        }

    If you want to use YAML format, create a config.yml with content:
        url: https://your.rancher.server/v1 // Or https://rancher.server/v1/projects/xxxx
        access_key: your_access_key
        secret_key: your_access_secret_key

    We accept environment variables as well:
        RANCHER_URL=https://your.rancher.server/v1   // Or https://rancher.server/v1/projects/xxxx
        RANCHER_ACCESS_KEY=your_access_key
        RANCHER_SECRET_KEY=your_access_secret_key
`
)

type Config struct {
	Command   string
	Container string
	Url  string
	Access_key      string
	Secret_key  string
}

type RancherAPI struct {
	Url string
	Access_key     string
	Secret_key string
}

type WebTerm struct {
	SocketConn *websocket.Conn
	ttyState   *terminal.State
	errChn     chan error
}

func (w *WebTerm) wsWrite() {
	var payload string
	var err error
	var keybuf [1]byte
	for {
		_, err = os.Stdin.Read(keybuf[0:1])
		if err != nil {
			if err == io.EOF { break }
			w.errChn <- err
			return
		}

		payload = base64.StdEncoding.EncodeToString(keybuf[0:1])
		err = w.SocketConn.WriteMessage(websocket.BinaryMessage, []byte(payload))
		if err != nil {
			if websocket.IsCloseError(err, websocket.CloseNormalClosure) {
				w.errChn <- nil
			} else {
				w.errChn <- err
			}
			return
		}
	}
}

func (w *WebTerm) wsRead() {
	var err error
	var raw []byte
	var out []byte
	var lastline string
	for {
		_, raw, err = w.SocketConn.ReadMessage()
		if err != nil {
			if websocket.IsCloseError(err, websocket.CloseNormalClosure) {
				scanner := bufio.NewScanner(strings.NewReader(string(out)))
				for scanner.Scan() {
					lastline = scanner.Text()
				}
				exitcode, err := strconv.Atoi(lastline)
				if err != nil { panic(err) }
				if exitcode != 0 {
					os.Exit(exitcode)
				}
				w.errChn <- nil
			} else {
				w.errChn <- err
			}
			return
		}
		out, err = base64.StdEncoding.DecodeString(string(raw))
		if err != nil {
			w.errChn <- err
			return
		}
		os.Stdout.Write(out)
	}
}

func (w *WebTerm) SetRawtty(isRaw bool) {
	if terminal.IsTerminal(int(os.Stdin.Fd())) {
		if isRaw {
			state, err := terminal.MakeRaw(int(os.Stdin.Fd()))
			if err != nil {
				panic(err)
			}
			w.ttyState = state
		} else {
			terminal.Restore(int(os.Stdin.Fd()), w.ttyState)
		}
	}
}

func (w *WebTerm) Run() {
	w.errChn = make(chan error)
	w.SetRawtty(true)

	go w.wsRead()
	go w.wsWrite()

	err := <-w.errChn
	w.SetRawtty(false)

	if err != nil {
		panic(err)
	}
}

func (r *RancherAPI) formatUrl() string {
	var Url string
	if r.Url[len(r.Url)-1:len(r.Url)] == "/" {
		Url = r.Url[0 : len(r.Url)-1]
	} else {
		Url = r.Url
	}
	if !strings.Contains(Url, "/v1/") {
		Url = Url + "/v1"
	}
	return Url
}

func (r *RancherAPI) makeReq(req *http.Request) (map[string]interface{}, error) {
	req.Header.Add("Accept", "application/json")
	req.Header.Add("Content-Type", "application/json")
	req.SetBasicAuth(r.Access_key, r.Secret_key)

	cli := http.Client{}
	resp, err := cli.Do(req)
	if err != nil {
		return nil, err
	}

	body, err := ioutil.ReadAll(resp.Body)
	if err != nil {
		return nil, err
	}
	resp.Body.Close()

	var tokenResp map[string]interface{}
	if err = json.Unmarshal(body, &tokenResp); err != nil {
		return nil, err
	}
	return tokenResp, nil
}

func (r *RancherAPI) containerUrl(name string) string {
	req, _ := http.NewRequest("GET", r.formatUrl()+"/containers/", nil)
	q := req.URL.Query()
	q.Add("name_like", strings.Replace(name, "*", "%", -1))
	q.Add("state", "running")
	q.Add("kind", "container")
	req.URL.RawQuery = q.Encode()
	resp, err := r.makeReq(req)
	if err != nil {
		fmt.Println("Failed to communicate with rancher API: " + err.Error())
		os.Exit(1)
	}
	if resp["type"] == "error" {
		fmt.Println(resp["message"])
		os.Exit(1)
	}
	data := resp["data"].([]interface{})
	var choice = 1
	if len(data) == 0 {
		fmt.Println("Container " + name + " not existed in system, not running, or you don't have access permissions.")
		os.Exit(1)
	}
	if len(data) > 1 {
		fmt.Println("We found more than one containers in system:")
		for i, _ctn := range data {
			ctn := _ctn.(map[string]interface{})
			if _, ok := ctn["data"]; ok {
				primaryIpAddress, _ := ctn["data"].(map[string]interface{})["fields"].(map[string]interface{})["primaryIpAddress"].(string)
				fmt.Println(fmt.Sprintf("[%d] %s, Container ID %s in project %s, IP Address %s on Host %s", i+1, ctn["name"].(string), ctn["id"].(string), ctn["accountId"].(string), primaryIpAddress, ctn["data"].(map[string]interface{})["fields"].(map[string]interface{})["dockerHostIp"].(string)))
			} else {
				fmt.Println(fmt.Sprintf("[%d] %s, Container ID %s in project %s, IP Address %s", i+1, ctn["name"].(string), ctn["id"].(string), ctn["accountId"].(string), ctn["primaryIpAddress"].(string)))
			}
		}
		fmt.Println("--------------------------------------------")
		fmt.Print("Which one you want to connect: ")
		fmt.Scan(&choice)
	}
	ctn := data[choice-1].(map[string]interface{})
	if _, ok := ctn["data"]; ok {
		primaryIpAddress, _ := ctn["data"].(map[string]interface{})["fields"].(map[string]interface{})["primaryIpAddress"].(string)
		fmt.Println(fmt.Sprintf("Target Container: %s, ID %s in project %s, Addr %s on Host %s", ctn["name"].(string), ctn["id"].(string), ctn["accountId"].(string), primaryIpAddress, ctn["data"].(map[string]interface{})["fields"].(map[string]interface{})["dockerHostIp"].(string)))
	} else {
		fmt.Println(fmt.Sprintf("Target Container: %s, ID %s in project %s, Addr %s", ctn["name"].(string), ctn["id"].(string), ctn["accountId"].(string), ctn["primaryIpAddress"].(string)))
	}
	return r.formatUrl() + fmt.Sprintf(
		"/containers/%s/", ctn["id"].(string))
}

func (r *RancherAPI) getWsUrl(url string, command string) string {
	if terminal.IsTerminal(int(os.Stdin.Fd())) {
		cols, rows, _ := terminal.GetSize(int(os.Stdin.Fd()))
		command = fmt.Sprintf(
			`{"attachStdin":true, "attachStdout":true,`+
				`"command":["/bin/sh", "-c", "TERM=xterm-256color; export TERM; `+
				`stty cols %d rows %d; `+
				`%s; echo $?"], "tty":true}`, cols, rows, command)
	} else {
		command = fmt.Sprintf(
			`{"attachStdin":true, "attachStdout":true,`+
				`"command":["/bin/sh", "-c", "TERM=xterm-256color; export TERM; `+
				`%s; echo $?"], "tty":true}`, command)
	}
	req, _ := http.NewRequest("POST", url+"?action=execute",strings.NewReader(command))
	resp, err := r.makeReq(req)
	if resp["type"] == "error" {
		fmt.Println(command)
		fmt.Println(resp["message"])
		os.Exit(1)
	}
	if err != nil {
		fmt.Println("Failed to get access token: ", err.Error())
		os.Exit(1)
	}
	return resp["url"].(string) + "?token=" + resp["token"].(string)
}

func (r *RancherAPI) getWSConn(wsUrl string) *websocket.Conn {
	url := r.formatUrl()
	header := http.Header{}
	header.Add("Origin", url)
	conn, _, err := websocket.DefaultDialer.Dial(wsUrl, header)
	if err != nil {
		fmt.Println("We couldn't connect to this container: ", err.Error())
		os.Exit(1)
	}
	return conn
}

func (r *RancherAPI) GetContainerConn(name string, command string) *websocket.Conn {
	fmt.Println("Searching for container " + name)
	url := r.containerUrl(name)
	fmt.Println("Getting access token")
	wsurl := r.getWsUrl(url, command)
	fmt.Println("SSH into container ...")
	return r.getWSConn(wsurl)
}

func ReadConfig() *Config {
	app := kingpin.New("rancherexec", USAGE)
	app.Author(AUTHOR)
	app.Version(VERSION)
	app.HelpFlag.Short('h')

	viper.SetDefault("url", "")
	viper.SetDefault("access_key", "")
	viper.SetDefault("secret_key", "")

	viper.SetConfigName("config")            // name of config file (without extension)
	viper.AddConfigPath(".")                 // call multiple times to add many search paths
	viper.AddConfigPath("$HOME/.rancherexec") // call multiple times to add many search paths
	viper.AddConfigPath("/etc/rancherexec/")  // path to look for the config file in
	viper.ReadInConfig()

	viper.SetEnvPrefix("rancher")
	viper.AutomaticEnv()

	var url = app.Flag("url", "Rancher server url, https://your.rancher.server/v1 or https://your.rancher.server/v1/projects/xxx.").Default(viper.GetString("url")).String()
	var access_key = app.Flag("access_key", "Rancher API access_key/accesskey.").Default(viper.GetString("access_key")).String()
	var secret_key = app.Flag("secret_key", "Rancher API secret_key/secret.").Default(viper.GetString("secret_key")).String()
	var command = app.Flag("command", "Command").Default(viper.GetString("command")).String()
	var container = app.Arg("container", "Container name, fuzzy match").Required().String()

	app.Parse(os.Args[1:])

	if *url == "" || *access_key == "" || *secret_key == "" || *container == "" || *command =="" {
		app.Usage(os.Args[1:])
		os.Exit(1)
	}

	return &Config{
		Command: *command,
		Container: *container,
		Url: *url,
		Access_key: *access_key,
		Secret_key: *secret_key,
	}

}

func main() {
	config := ReadConfig()
	rancher := RancherAPI{
		Url: config.Url,
		Access_key: config.Access_key,
		Secret_key: config.Secret_key,
	}
	conn := rancher.GetContainerConn(config.Container, config.Command)

	wt := WebTerm{
		SocketConn: conn,
	}
	wt.Run()
}
