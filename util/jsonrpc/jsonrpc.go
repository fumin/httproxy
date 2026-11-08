package jsonrpc

import (
	"bytes"
	"context"
	jsonv1 "encoding/json"
	"encoding/json/jsontext"
	"encoding/json/v2"
	"fmt"
	"io"
	"net/http"
	"reflect"
	"time"

	"github.com/pkg/errors"
)

var jsonOpts = jsonv1.FormatDurationAsNano(true)

type RPCError struct {
	Code    int    `json:"code"`
	Message string `json:"message"`
}

func (e RPCError) Error() string {
	return e.Message
}

func Do(client *http.Client, url, method string, params, result any) error {
	// Prepare request body.
	reqJ := struct {
		ID      string `json:"id"`
		JSONRPC string `json:"jsonrpc"`
		Method  string `json:"method"`
		Params  any    `json:"params"`
	}{
		ID: "0", JSONRPC: "2.0", Method: method, Params: params,
	}
	reqBody := new(bytes.Buffer)
	if err := json.MarshalWrite(reqBody, reqJ, jsonOpts); err != nil {
		return errors.Wrap(err, "")
	}

	// Do request.
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	req, err := http.NewRequestWithContext(ctx, "POST", url, reqBody)
	if err != nil {
		return errors.Wrap(err, "")
	}
	req.Header.Set("Content-Type", "application/json")
	resp, err := client.Do(req)
	if err != nil {
		return errors.Wrap(err, "")
	}
	defer resp.Body.Close()

	// Parse response.
	respJ := response{Result: result}
	if err := json.UnmarshalRead(resp.Body, &respJ, jsonOpts); err != nil {
		return errors.Wrap(err, "")
	}
	if respJ.Error != nil {
		return respJ.Error
	}
	return nil
}

type Method func(any) (any, error)
type MethodParams struct {
	Method Method
	Params any
}

func NewHandler(methods map[string]MethodParams) func(w http.ResponseWriter, r *http.Request) {
	return func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		result, err := handle(methods, r.Body)
		if err != nil {
			json.MarshalWrite(w, response{Error: err}, jsonOpts)
			return
		}
		json.MarshalWrite(w, response{Result: result}, jsonOpts)
	}
}

type response struct {
	Error  *RPCError `json:"error,omitzero"`
	Result any       `json:"result,omitzero"`
}

func handle(methods map[string]MethodParams, body io.Reader) (any, *RPCError) {
	// Parse method.
	req := struct {
		Method string         `json:"method"`
		Params jsontext.Value `json:"params"`
	}{}
	if err := json.UnmarshalRead(body, &req, jsonOpts); err != nil {
		return nil, &RPCError{Code: -32600, Message: err.Error()}
	}
	m, ok := methods[req.Method]
	if !ok {
		return nil, &RPCError{Code: -32601}
	}

	// Parse params.
	paramsPtr := reflect.New(reflect.TypeOf(m.Params))
	if len(req.Params) > 0 {
		if err := json.Unmarshal(req.Params, paramsPtr.Interface(), jsonOpts); err != nil {
			return nil, &RPCError{Code: -32602, Message: err.Error()}
		}
	}
	params := paramsPtr.Elem().Interface()

	// Invoke method.
	result, err := m.Method(params)
	if err != nil {
		msg := fmt.Sprintf("%+v", err)
		return nil, &RPCError{Code: -1, Message: msg}
	}
	return result, nil
}
