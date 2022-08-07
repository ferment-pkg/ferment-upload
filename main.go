package main

import (
	"context"
	"crypto/hmac"
	"crypto/sha256"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"io"
	"io/fs"
	"log"
	"net/http"
	"os"
	"os/exec"
	"os/signal"
	"strings"
	"syscall"

	firebase "firebase.google.com/go/v4"
	"github.com/gorilla/websocket"
	"google.golang.org/api/option"
)

func main() {
	//start http server
	home := os.Getenv("HOME")
	fmt.Println("Starting Logger")
	os.Mkdir("logs", 0777)
	//init firebase app
	os.MkdirAll(fmt.Sprintf("%s/.ferment-uploader/logs", home), 0777)
	logFile, err := os.OpenFile(home+"/.ferment-uploader/logs/server.log", os.O_CREATE|os.O_WRONLY|os.O_APPEND, 0666)
	if err != nil {
		fmt.Println("Failed to open log file")
		os.Exit(1)
	}
	writer := io.MultiWriter(os.Stdout, logFile)
	logger := log.New(writer, "", log.LstdFlags|log.Lmsgprefix)
	sigChan := make(chan os.Signal)
	logger.Println("Starting server")
	reader(logger)
	ctx := context.Background()
	opt := option.WithCredentialsFile(os.Getenv("KEYS_PATH"))
	app, err := firebase.NewApp(ctx, &firebase.Config{
		ProjectID:     os.Getenv("PROJECT_ID"),
		StorageBucket: os.Getenv("STORAGE_BUCKET"),
	}, opt)
	if err != nil {
		logger.Panicf("Failed to initialize firebase app: %s", err)
	}
	storageClient, err := app.Storage(ctx)
	if err != nil {
		logger.Panicf("Failed to initialize firebase storage: %s", err)
	}
	bucket, err := storageClient.DefaultBucket()
	if err != nil {
		logger.Panicf("Failed to initialize firebase storage bucket: %s", err)
	}
	logger.Println("Firebase Succesfully initialized")
	//retreive all files in the bucket

	go func() {
		//handle SIGINT and SIGTERM

		signal.Notify(sigChan, syscall.SIGINT, syscall.SIGTERM)
		<-sigChan
		logger.Println("Received SIGINT or SIGTERM. Shutting down...")
		os.Exit(0)
	}()
	type CachedRes struct {
		Name string `json:"name"`
		File string `json:"file"`
	}
	dir, err := os.ReadDir(home + "/.ferment-uploader/cache/")
	if err != nil {
		logger.Println("Cache Directory Not Created")
	}

	var cachedRes []CachedRes
	for _, file := range dir {
		if file.Name() == "cached.json" {
			file, err := os.Open(file.Name())
			if err != nil {
				logger.Println("Failed to open cached.json")
				break
			}
			defer file.Close()
			logger.Println("Cached File Found, now reading to continue firebase upload")
			decoder := json.NewDecoder(file)
			err = decoder.Decode(&cachedRes)
			if err != nil {
				logger.Println("Failed to decode cached.json")
				os.Remove("cached.json")
				break
			}
		}
		go func(file fs.DirEntry) {
			var message CachedRes
			for _, res := range cachedRes {
				if res.File == file.Name() {
					message = res
					break
				}
			}
			if message.File == "" {
				os.Remove(file.Name())
				return

			}
			bytes, err := os.ReadFile(home + "/.ferment-uploader/cache/" + message.File)
			if err != nil {
				logger.Panicf("Failed to read file: %s", err.Error())
			}
			writer := bucket.Object(fmt.Sprintf("%s/%s", message.Name, message.File)).NewWriter(ctx)
			writer.Write(bytes)
			if writer.Close() != nil {
				logger.Panicf("Failed to upload file: %s", err)
			}
			os.Remove(home + "/.ferment-uploader/cache/" + message.File)
			logger.Printf("Successfully uploaded file: %s ", message.File)
		}(file)
	}

	http.HandleFunc("/", func(w http.ResponseWriter, r *http.Request) {
		logger.Println("Received request")
		upgrader := websocket.Upgrader{
			EnableCompression: true,
			ReadBufferSize:    1024 * 1024 * 50,
		}
		conn, err := upgrader.Upgrade(w, r, nil)
		if err != nil {
			logger.Panicln("Failed to upgrade connection")
		}
		type Message struct {
			File string `json:"file"`
			Part int    `json:"part"`
			Of   int    `json:"of"`
			Data string `json:"data"`
			Name string `json:"name"`
		}
		type Response struct {
			Status  int    `json:"status"`
			Message string `json:"message"`
		}

		conn.SetCompressionLevel(9)
		for {

			_, p, err := conn.ReadMessage()
			if err != nil {
				logger.Panic("Failed to read message", err)
				conn.WriteJSON(Response{Status: 500, Message: "Failed to read message"})
				return
			}
			//check if p is larger than 10mb
			logger.Println(len(p) / 1024 / 1024)
			if len(p) > 1024*1024*50 {
				conn.WriteJSON(Response{Status: 500, Message: "Message too large, max 10mb"})
				return
			}
			var message Message
			err = json.Unmarshal(p, &message)
			if err != nil {
				conn.WriteJSON(Response{Status: 500, Message: "Failed to parse message"})
				break
			}
			//check all of the fields are present
			if message.File == "" || message.Part == 0 || message.Of == 0 || message.Data == "" || message.Name == "" {
				conn.WriteJSON(Response{Status: 400, Message: "Missing fields"})
				conn.Close()
				break
			}
			os.Mkdir(home+"/.ferment-uploader/cache", 0777)
			if message.Part == 1 {
				os.Remove(home + "/.ferment-uploader/cache/" + message.File)
			}
			file, err := os.OpenFile(fmt.Sprintf(home+"/.ferment-uploader/cache/%s", message.File), os.O_APPEND|os.O_CREATE|os.O_WRONLY, 0644)
			if err != nil {
				logger.Println("Failed to open file")
				conn.WriteJSON(Response{Status: 500, Message: "Failed to open file"})
				conn.Close()
				break
			}
			//decode base64
			decoded, err := base64.StdEncoding.DecodeString(message.Data)
			if err != nil {
				conn.WriteJSON(Response{Status: 400, Message: "Data is not of base64 format"})
				conn.Close()
			}
			file.Write(decoded)
			if message.Part == message.Of {
				go func() {
					//read file and upload to firebase
					bytes, err := os.ReadFile(home + "/.ferment-uploader/cache/" + message.File)
					if err != nil {
						logger.Panicf("Failed to read file: %s", err.Error())
					}
					writer := bucket.Object(fmt.Sprintf("%s/%s", message.Name, message.File)).NewWriter(ctx)
					writer.Write(bytes)
					if writer.Close() != nil {
						logger.Panicf("Failed to upload file: %s", err)
					}
					os.Remove(home + "/.ferment-uploader/cache/" + message.File)
					logger.Printf("Successfully uploaded file: %s ", message.File)
					file, err := os.ReadFile(home + "/.ferment-uploader/cache/cached.json")
					if err != nil {
						logger.Panicf("Failed to read cached.json: %s", err.Error())
					}
					var cachedRes []CachedRes
					json.Unmarshal(file, &cachedRes)
					for i, res := range cachedRes {
						if res.File == message.File {
							//remove file from cached.json
							cachedRes = append(cachedRes[:i], cachedRes[i+1:]...)
							break
						}
					}

					out, err := json.Marshal(cachedRes)
					if err != nil {
						logger.Panicf("Failed to marshal cachedRes: %s", err)
					}
					err = os.WriteFile(home+"/.ferment-uploader/cache/cached.json", out, 0644)
					if err != nil {
						logger.Panicf("Failed to write cached.json: %s", err.Error())
					}
					logger.Println("Removed from cached.json")

				}()
				file, err := os.ReadFile(home + "/.ferment-uploader/cache/cached.json")
				if err != nil {
					_, err := os.Create(home + "/.ferment-uploader/cache/cached.json")
					if err != nil {
						logger.Panicf("Failed to create cached.json: %s", err.Error())
					}
					file, err = os.ReadFile(home + "/.ferment-uploader/cache/cached.json")
					if err != nil {
						logger.Panicf("Failed to read cached.json: %s", err.Error())

					}

				}
				var cachedRes []CachedRes
				json.Unmarshal(file, &cachedRes)
				cachedRes = append(cachedRes, CachedRes{Name: message.Name, File: message.File})
				out, err := json.Marshal(cachedRes)
				if err != nil {
					logger.Panicf("Failed to marshal cached.json: %s", err.Error())
				}
				err = os.WriteFile(home+"/.ferment-uploader/cache/cached.json", out, 0644)
				if err != nil {
					logger.Panicf("Failed to write cached.json: %s", err.Error())
				}
				conn.WriteJSON(Response{Status: 200, Message: "File uploaded to local server successfully, asynchronusly uploading to firebase"})
				break
			} else {
				conn.WriteJSON(Response{Status: 200, Message: fmt.Sprintf("Part %d of %d Uploaded Succesfully", message.Part, message.Of)})
			}

		}
	})
	payload := ghPayload(logger)
	http.HandleFunc("/ghpayload", payload)
	//keep code running
	logger.Println("Listening on port " + os.Getenv("PORT"))
	http.ListenAndServe(":"+(os.Getenv("PORT")), nil)
}

func reader(logger *log.Logger) {
	file, err := os.Open(".env")
	if err != nil {
		logger.Println(".env file not found assuming env already set")
		return
	}
	defer file.Close()
	//store file content in a byte array
	var content []byte
	stat, err := file.Stat()
	if err != nil {
		logger.Panicf("Failed to get file stat: %s", err)
	}
	for i := 0; i < int(stat.Size()); i++ {
		//read a single byte
		byte := make([]byte, 1)
		_, err := file.Read(byte)
		if err == io.EOF {
			break
		}
		if err != nil {
			logger.Panicf("Failed to read .env file: %s", err)
		}
		content = append(content, byte...)
	}
	//split content by newline
	lines := strings.Split(string(content), "\n")
	for _, line := range lines {
		//split line by =
		str := strings.Replace(line, "=", " ", 1)
		parts := strings.Split(str, " ")
		if len(parts) == 2 {
			//make sure to remve speech marks
			os.Setenv(parts[0], strings.Replace(parts[1], "\"", "", -1))
			//set environment variable
		}
	}
}
func ghPayload(logger *log.Logger) func(w http.ResponseWriter, r *http.Request) {
	type Response struct {
		Status  int    `json:"status"`
		Message string `json:"message"`
	}
	return func(w http.ResponseWriter, r *http.Request) {
		switch r.Method {
		case "POST":
			break
		default:
			w.WriteHeader(http.StatusMethodNotAllowed)
			w.Write([]byte("Cannot " + r.Method + " to this endpoint"))
			return

		}
		secret := os.Getenv("GH_SECRET")
		if secret == "" {
			logger.Panicf("GH_SECRET not set")
		}
		if r.Header.Get("X-Hub-Signature-256") == "" {
			res, err := json.Marshal(Response{Status: 400, Message: "X-Hub-Signature-256 not set"})
			if err != nil {
				logger.Panicf("Failed to marshal response: %s", err)
				w.Write([]byte("Failed to marshal response"))
				w.WriteHeader(500)
			}
			w.WriteHeader(http.StatusBadRequest)
			w.Write(res)
			return
		}
		switch r.Header.Get("X-Github-Event") {
		case "push":
			break
		case "ping":
			w.Write([]byte("pong"))
			return
		default:
			res, err := json.Marshal(Response{Status: 400, Message: "X-Github-Event not set"})
			if err != nil {
				logger.Panicf("Failed to marshal response: %s", err)
				w.Write([]byte("Failed to marshal response"))
				w.WriteHeader(500)
			}
			w.WriteHeader(http.StatusBadRequest)
			w.Write(res)
			return
		}
		rawBody := r.Body
		defer rawBody.Close()
		body, err := io.ReadAll(rawBody)
		if err != nil {
			logger.Panicf("Failed to read body: %s", err)
			w.Write([]byte("Failed to read body"))
			w.WriteHeader(500)
			return
		}
		//verify signature
		sig := r.Header.Get("X-Hub-Signature-256")
		if !verifySignature(body, secret, sig, logger) {
			res, err := json.Marshal(Response{Status: 400, Message: "Signature verification failed"})
			if err != nil {
				logger.Panicf("Failed to marshal response: %s", err)
				w.Write([]byte("Failed to marshal response"))
				w.WriteHeader(500)
			}
			w.WriteHeader(http.StatusBadRequest)
			w.Write(res)
			return
		}
		//run git pull through shell
		cmd := exec.Command("git", "pull")
		err = cmd.Run()
		if err != nil {
			logger.Panicf("Failed to run git pull: %s", err)
			w.Write([]byte("Failed to run git pull"))
			w.WriteHeader(500)
			return
		}
		w.Write([]byte("OK"))
		syscall.Kill(syscall.Getpid(), syscall.SIGINT)

	}

}
func verifySignature(body []byte, secret string, sha string, logger *log.Logger) bool {
	h := hmac.New(sha256.New, []byte(secret))
	h.Write(body)
	expectedMAC := h.Sum(nil)
	actualMAC, err := hex.DecodeString(sha)
	if err != nil {
		logger.Panicf("Failed to decode hex: %s", err)
	}
	return hmac.Equal(actualMAC, expectedMAC)
}
