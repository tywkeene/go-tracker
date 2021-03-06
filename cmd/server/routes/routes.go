package routes

import (
	"compress/gzip"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strings"
	"time"

	"github.com/tywkeene/go-agent/cmd/server/auth"
	"github.com/tywkeene/go-agent/cmd/server/db"
	"github.com/tywkeene/go-agent/cmd/server/options"
	"github.com/tywkeene/go-agent/cmd/server/utils"

	log "github.com/Sirupsen/logrus"
	"github.com/satori/go.uuid"
)

var ErrAlreadyOnline = fmt.Errorf("that device is already online")
var ErrAlreadyOffline = fmt.Errorf("that device is already offline")
var ErrGettingStatus = fmt.Errorf("could not retrieve device status")

type gzipResponseWriter struct {
	io.Writer
	http.ResponseWriter
}

func (w gzipResponseWriter) Write(b []byte) (int, error) {
	return w.Writer.Write(b)
}

//Handle and make sure the client wants or can handle gzip, and replace the writer if it
//can, if not, simply use the normal http.ResponseWriter
func GzipHandler(fn http.HandlerFunc) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		if strings.Contains(r.Header.Get("Accept-Encoding"), "application/x-gzip") == false {
			fn(w, r)
			return
		}
		w.Header().Set("Content-Encoding", "application/x-gzip")
		gz := gzip.NewWriter(w)
		defer gz.Close()
		gzr := gzipResponseWriter{Writer: gz, ResponseWriter: w}
		fn(gzr, r)
	}
}

func LogHttp(r *http.Request) {
	log.Infof("%s %s %s %s", r.Method, r.URL, r.RemoteAddr, r.UserAgent())
}

//Checks a request header and ensures it is allowed, otherwise it will set the Allow http header
// and return HTTP 405 Method Not Allowed
func validateRequestMethod(errHandle *utils.HttpErrorHandler, allowed string) bool {
	if strings.Contains(allowed, errHandle.Request.Method) == false {
		errHandle.Response.Header().Set("Allow", allowed)
		utils.SetResponseHeaders(errHandle.Response, http.StatusMethodNotAllowed)
		errHandle.Handle(fmt.Errorf("Method not allowed"), http.StatusMethodNotAllowed, utils.ErrorActionErr)
		return false
	}
	return true
}

//GetQueryValue() takes a name of a key:value pair to fetch from a URL encoded query,
//a http.ResponseWriter 'w', and a http.Request 'r'. In the event that an error is encountered
//the error will be returned to the client via logging facilities that use 'w' and 'r'
func GetQueryValue(name string, w http.ResponseWriter, r *http.Request) (string, error) {
	query, err := url.ParseQuery(r.URL.RawQuery)
	if query == nil || err != nil {
		return "", err
	}
	return query.Get(name), nil
}

// This is neat: https://coderwall.com/p/cp5fya/measuring-execution-time-in-go
func timeTrack(start time.Time, name string) {
	if options.Config.Server.TimetrackAPI == true {
		elapsed := time.Since(start)
		log.Infof("%s took %s", name, elapsed)
	}
}

func registerHandle(w http.ResponseWriter, r *http.Request) {
	LogHttp(r)
	defer r.Body.Close()
	defer timeTrack(time.Now(), "registerHandle")
	errHandle := utils.NewHttpErrorHandle("registerHandle", w, r)
	if validateRequestMethod(errHandle, "POST") == false {
		return
	}

	decoder := json.NewDecoder(r.Body)
	var registerAuth *db.DeviceRegister
	err := decoder.Decode(&registerAuth)
	if errHandle.Handle(err, http.StatusInternalServerError, utils.ErrorActionErr) == true {
		return
	}

	err = auth.ValidateRegisterAuth(registerAuth.AuthStr)
	if errHandle.Handle(err, http.StatusUnauthorized, utils.ErrorActionErr) == true {
		return
	}
	deviceUUID := uuid.NewV4().String()
	uuidJson, err := json.Marshal(deviceUUID)
	if errHandle.Handle(err, http.StatusInternalServerError, utils.ErrorActionErr) == true {
		return
	}

	addr := strings.Split(r.RemoteAddr, ":")
	device := &db.Device{
		UUID:     deviceUUID,
		Address:  addr[0],
		AuthStr:  registerAuth.AuthStr,
		Hostname: registerAuth.Hostname,
		Online:   false,
		LastSeen: nil,
	}

	err = db.HandleRegister(device)
	if errHandle.Handle(err, http.StatusUnauthorized, utils.ErrorActionErr) == true {
		return
	}

	log.Infof("Hostname '%s' successfully registered (authstr:%s) (uuid:%s)",
		device.Hostname, registerAuth.AuthStr, device.UUID)
	utils.SetResponseHeaders(w, http.StatusOK)
	io.WriteString(w, string(uuidJson))
}

func loginHandle(w http.ResponseWriter, r *http.Request) {
	LogHttp(r)
	defer r.Body.Close()
	defer timeTrack(time.Now(), "loginHandle")
	errHandle := utils.NewHttpErrorHandle("loginHandle", w, r)
	if validateRequestMethod(errHandle, "POST") == false {
		return
	}
	decoder := json.NewDecoder(r.Body)
	var device *db.Device
	err := decoder.Decode(&device)
	if errHandle.Handle(err, http.StatusInternalServerError, utils.ErrorActionErr) == true {
		return
	}

	online, err := db.IsDeviceOnline(device)
	if err != nil {
		log.Error(err)
		errHandle.Handle(ErrGettingStatus, http.StatusInternalServerError, utils.ErrorActionErr)
		return
	}
	if online == true {
		errHandle.Handle(ErrAlreadyOnline, http.StatusBadRequest, utils.ErrorActionInfo)
		return
	}

	err = db.HandleLogin(device)
	if errHandle.Handle(err, http.StatusUnauthorized, utils.ErrorActionErr) == true {
		return
	}
	utils.SetResponseHeaders(w, http.StatusOK)
	log.Infof("Device '%s' logged in [authstr:%s] [uuid:%s]",
		device.Hostname, device.AuthStr, device.UUID)
}

func logoffHandle(w http.ResponseWriter, r *http.Request) {
	LogHttp(r)
	defer r.Body.Close()
	defer timeTrack(time.Now(), "logoffHandle")
	errHandle := utils.NewHttpErrorHandle("logoffHandle", w, r)
	if validateRequestMethod(errHandle, "POST") == false {
		return
	}
	decoder := json.NewDecoder(r.Body)
	var device *db.Device
	err := decoder.Decode(&device)
	if errHandle.Handle(err, http.StatusInternalServerError, utils.ErrorActionErr) == true {
		return
	}

	online, err := db.IsDeviceOnline(device)
	if err != nil {
		log.Error(err)
		errHandle.Handle(ErrGettingStatus, http.StatusInternalServerError, utils.ErrorActionErr)
		return
	}
	if online == false {
		errHandle.Handle(ErrAlreadyOffline, http.StatusBadRequest, utils.ErrorActionWarn)
		return
	}

	err = db.HandleLogoff(device)
	if errHandle.Handle(err, http.StatusUnauthorized, utils.ErrorActionErr) == true {
		return
	}
	utils.SetResponseHeaders(w, http.StatusOK)
	log.Infof("Device '%s' logged off [authstr:%s] [uuid:%s]",
		device.Hostname, device.AuthStr, device.UUID)
}

func pingHandle(w http.ResponseWriter, r *http.Request) {
	LogHttp(r)
	defer r.Body.Close()
	defer timeTrack(time.Now(), "pingHandle")
	errHandle := utils.NewHttpErrorHandle("pingHandle", w, r)
	if validateRequestMethod(errHandle, "POST") == false {
		return
	}
	decoder := json.NewDecoder(r.Body)
	var device *db.Device
	err := decoder.Decode(&device)
	if errHandle.Handle(err, http.StatusInternalServerError, utils.ErrorActionErr) == true {
		return
	}

	err = db.HandlePing(device)
	if errHandle.Handle(err, http.StatusInternalServerError, utils.ErrorActionErr) == true {
		return
	}
	utils.SetResponseHeaders(w, http.StatusOK)
	log.Infof("Device '%s' has pinged [authstr:%s] [uuid:%s]",
		device.Hostname, device.AuthStr, device.UUID)
}

func statusHandle(w http.ResponseWriter, r *http.Request) {
	LogHttp(r)
	defer r.Body.Close()
	defer timeTrack(time.Now(), "statusHandle")
	errHandle := utils.NewHttpErrorHandle("statusHandle", w, r)
	if validateRequestMethod(errHandle, "POST") == false {
		return
	}
	decoder := json.NewDecoder(r.Body)
	var device *db.Device
	err := decoder.Decode(&device)
	if errHandle.Handle(err, http.StatusInternalServerError, utils.ErrorActionErr) == true {
		return
	}

	registered, err := db.AuthorizeDevice(device)
	if err != nil {
		log.Error(err)
	}

	if registered == false {
		errHandle.Handle(db.ErrUnauthorizedDevice, http.StatusUnauthorized, utils.ErrorActionErr)
		return
	}
	utils.SetResponseHeaders(w, http.StatusOK)
	log.Infof("Device '%s' has sent a status check [authstr:%s] [uuid:%s]",
		device.Hostname, device.AuthStr, device.UUID)
	return
}

func RegisterHandles() {
	http.HandleFunc("/register", GzipHandler(registerHandle))
	http.HandleFunc("/ping", GzipHandler(pingHandle))
	http.HandleFunc("/login", GzipHandler(loginHandle))
	http.HandleFunc("/logoff", GzipHandler(logoffHandle))
	http.HandleFunc("/status", GzipHandler(statusHandle))
}

func Launch() {
	serverOptions := options.Config.Server
	panic(http.ListenAndServeTLS(":"+serverOptions.Port, serverOptions.SSLCert, serverOptions.SSLKey, nil))
}
