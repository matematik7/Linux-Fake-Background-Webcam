package main

import (
	"bytes"
	"context"
	"encoding/json"
	"io/ioutil"
	"log"
	"net/http"
	"os/exec"
	"sync"
	"time"

	"github.com/pkg/errors"
)

type statusResponse struct {
	On bool `json:"on"`
}

type status struct {
	mtx sync.Mutex
	on  bool
}

func (s *status) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	s.mtx.Lock()
	data := statusResponse{On: s.on}
	s.mtx.Unlock()

	w.Header().Set("Content-Type", "application/json")
	encoder := json.NewEncoder(w)
	if err := encoder.Encode(data); err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
	}
}

func (s *status) Worker() {
	for range time.NewTicker(time.Second).C {
		err := exec.Command("fuser", "/dev/video2").Run()

		s.mtx.Lock()
		s.on = err == nil
		s.mtx.Unlock()
	}
}

type server struct {
	mtx         sync.Mutex
	response    []byte
	request     []byte
	lastRequest time.Time
}

func (s *server) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	defer r.Body.Close()
	request, err := ioutil.ReadAll(r.Body)
	if err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}

	s.mtx.Lock()
	s.request = request
	s.lastRequest = time.Now()
	response := s.response
	s.mtx.Unlock()

	if response == nil {
		http.Error(w, "not ready yet", http.StatusServiceUnavailable)
		return
	}

	if _, err := w.Write(response); err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}
}

func (s *server) Worker() {
	for range time.NewTicker(time.Millisecond * 200).C {
		s.mtx.Lock()
		request := s.request
		lastRequest := s.lastRequest
		s.mtx.Unlock()

		if time.Since(lastRequest) > time.Second {
			continue
		}

		go func(request []byte) {
			response, err := s.makeRequest(request)
			if err != nil {
				log.Println(err)
				return
			}

			s.mtx.Lock()
			s.response = response
			s.mtx.Unlock()
		}(request)
	}
}

func (s *server) makeRequest(request []byte) ([]byte, error) {
	if request == nil {
		return nil, errors.New("waiting for request")
	}

	ctx, cancel := context.WithTimeout(context.Background(), time.Second)
	defer cancel()

	req, err := http.NewRequestWithContext(ctx, "POST", "http://172.29.163.21:8123/", bytes.NewReader(request))
	if err != nil {
		return nil, err
	}
	req.Header.Set("Content-Type", "application/octet-stream")
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()
	if resp.StatusCode/100 != 2 {
		return nil, errors.Errorf("Response status: %v", resp.StatusCode)
	}

	response, err := ioutil.ReadAll(resp.Body)
	if err != nil {
		return nil, err
	}

	return response, nil
}

func main() {
	status := &status{}
	go status.Worker()
	http.Handle("/status", status)

	s := &server{}
	go s.Worker()
	http.Handle("/", s)
	log.Fatal(http.ListenAndServe(":8124", nil))
}
