package main

import (
	"encoding/json"
	"fmt"
	"io"
	"log"
	"net/http"
	"os"

	admissionv1 "k8s.io/api/admission/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
)

const (
	certFile = "/etc/webhook/tls/tls.crt"
	keyFile  = "/etc/webhook/tls/tls.key"
	port     = 8443
)

func main() {
	mux := http.NewServeMux()
	mux.HandleFunc("/mutate", handleMutate)
	mux.HandleFunc("/healthz", func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusOK)
	})

	addr := fmt.Sprintf(":%d", port)
	log.Printf("Starting webhook server on %s", addr)

	if _, err := os.Stat(certFile); err != nil {
		log.Fatalf("TLS cert not found at %s: %v", certFile, err)
	}

	if err := http.ListenAndServeTLS(addr, certFile, keyFile, mux); err != nil {
		log.Fatalf("Failed to start server: %v", err)
	}
}

func handleMutate(w http.ResponseWriter, r *http.Request) {
	body, err := io.ReadAll(r.Body)
	if err != nil {
		http.Error(w, "failed to read body", http.StatusBadRequest)
		return
	}

	var review admissionv1.AdmissionReview
	if err := json.Unmarshal(body, &review); err != nil {
		http.Error(w, "failed to decode admission review", http.StatusBadRequest)
		return
	}

	patch := `[{"op":"add","path":"/metadata/annotations/e2e-webhook.example.com~1processed","value":"true"}]`
	patchType := admissionv1.PatchTypeJSONPatch

	review.Response = &admissionv1.AdmissionResponse{
		UID:       review.Request.UID,
		Allowed:   true,
		PatchType: &patchType,
		Patch:     []byte(patch),
		Result: &metav1.Status{
			Message: "processed by e2e webhook",
		},
	}

	resp, err := json.Marshal(review)
	if err != nil {
		http.Error(w, "failed to encode response", http.StatusInternalServerError)
		return
	}

	w.Header().Set("Content-Type", "application/json")
	w.Write(resp)
}
