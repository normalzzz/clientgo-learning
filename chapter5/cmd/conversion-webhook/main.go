package main

import (
	"crypto/tls"
	"encoding/json"
	"flag"
	"fmt"
	"log"
	"net/http"

	appsconversion "github.com/normalzzz/clientgo-learning/chapter5/pkg/apis/apps/conversion"
	appsv1 "github.com/normalzzz/clientgo-learning/chapter5/pkg/apis/apps/v1"
	appsv1alpha1 "github.com/normalzzz/clientgo-learning/chapter5/pkg/apis/apps/v1alpha1"
	apiextensionsv1 "k8s.io/apiextensions-apiserver/pkg/apis/apiextensions/v1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
)

func main() {
	addr := flag.String("addr", ":9443", "webhook listen address")
	certFile := flag.String("cert-file", "/tls/tls.crt", "TLS certificate file")
	keyFile := flag.String("key-file", "/tls/tls.key", "TLS private key file")
	flag.Parse()

	mux := http.NewServeMux()
	mux.HandleFunc("/healthz", func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write([]byte("ok"))
	})
	mux.HandleFunc("/convert", conversionHandler)

	server := &http.Server{
		Addr:      *addr,
		Handler:   mux,
		TLSConfig: &tls.Config{MinVersion: tls.VersionTLS12},
	}

	log.Printf("starting conversion webhook on %s", *addr)
	if err := server.ListenAndServeTLS(*certFile, *keyFile); err != nil {
		log.Fatalf("conversion webhook stopped: %v", err)
	}
}

func conversionHandler(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
		return
	}

	var review apiextensionsv1.ConversionReview
	if err := json.NewDecoder(r.Body).Decode(&review); err != nil {
		http.Error(w, fmt.Sprintf("decode ConversionReview: %v", err), http.StatusBadRequest)
		return
	}

	response := convertReview(review.Request)
	out := apiextensionsv1.ConversionReview{
		TypeMeta: metav1.TypeMeta{
			APIVersion: "apiextensions.k8s.io/v1",
			Kind:       "ConversionReview",
		},
		Response: response,
	}

	w.Header().Set("Content-Type", "application/json")
	if err := json.NewEncoder(w).Encode(out); err != nil {
		log.Printf("encode ConversionReview response: %v", err)
	}
}

func convertReview(request *apiextensionsv1.ConversionRequest) *apiextensionsv1.ConversionResponse {
	response := &apiextensionsv1.ConversionResponse{
		Result: metav1.Status{Status: metav1.StatusSuccess},
	}
	if request == nil {
		response.Result = statusFailure("missing conversion request")
		return response
	}

	response.UID = request.UID
	response.ConvertedObjects = make([]runtime.RawExtension, 0, len(request.Objects))
	for _, obj := range request.Objects {
		converted, err := convertObject(obj, request.DesiredAPIVersion)
		if err != nil {
			response.Result = statusFailure(err.Error())
			response.ConvertedObjects = nil
			return response
		}
		response.ConvertedObjects = append(response.ConvertedObjects, converted)
	}
	return response
}

func convertObject(raw runtime.RawExtension, desiredAPIVersion string) (runtime.RawExtension, error) {
	var typeMeta metav1.TypeMeta
	if err := json.Unmarshal(raw.Raw, &typeMeta); err != nil {
		return runtime.RawExtension{}, fmt.Errorf("decode TypeMeta: %w", err)
	}
	if typeMeta.Kind != "Website" {
		return runtime.RawExtension{}, fmt.Errorf("unsupported kind %q", typeMeta.Kind)
	}

	switch desiredAPIVersion {
	case appsv1.SchemeGroupVersion.String():
		return convertObjectToV1(raw, typeMeta.APIVersion)
	case appsv1alpha1.SchemeGroupVersion.String():
		return convertObjectToV1Alpha1(raw, typeMeta.APIVersion)
	default:
		return runtime.RawExtension{}, fmt.Errorf("unsupported desired apiVersion %q", desiredAPIVersion)
	}
}

func convertObjectToV1(raw runtime.RawExtension, sourceAPIVersion string) (runtime.RawExtension, error) {
	switch sourceAPIVersion {
	case appsv1.SchemeGroupVersion.String():
		var website appsv1.Website
		if err := json.Unmarshal(raw.Raw, &website); err != nil {
			return runtime.RawExtension{}, fmt.Errorf("decode v1 Website: %w", err)
		}
		website.APIVersion = appsv1.SchemeGroupVersion.String()
		website.Kind = "Website"
		return marshalRaw(&website)
	case appsv1alpha1.SchemeGroupVersion.String():
		var website appsv1alpha1.Website
		if err := json.Unmarshal(raw.Raw, &website); err != nil {
			return runtime.RawExtension{}, fmt.Errorf("decode v1alpha1 Website: %w", err)
		}
		return marshalRaw(appsconversion.WebsiteV1Alpha1ToV1(&website))
	default:
		return runtime.RawExtension{}, fmt.Errorf("unsupported source apiVersion %q", sourceAPIVersion)
	}
}

func convertObjectToV1Alpha1(raw runtime.RawExtension, sourceAPIVersion string) (runtime.RawExtension, error) {
	switch sourceAPIVersion {
	case appsv1alpha1.SchemeGroupVersion.String():
		var website appsv1alpha1.Website
		if err := json.Unmarshal(raw.Raw, &website); err != nil {
			return runtime.RawExtension{}, fmt.Errorf("decode v1alpha1 Website: %w", err)
		}
		website.APIVersion = appsv1alpha1.SchemeGroupVersion.String()
		website.Kind = "Website"
		return marshalRaw(&website)
	case appsv1.SchemeGroupVersion.String():
		var website appsv1.Website
		if err := json.Unmarshal(raw.Raw, &website); err != nil {
			return runtime.RawExtension{}, fmt.Errorf("decode v1 Website: %w", err)
		}
		return marshalRaw(appsconversion.WebsiteV1ToV1Alpha1(&website))
	default:
		return runtime.RawExtension{}, fmt.Errorf("unsupported source apiVersion %q", sourceAPIVersion)
	}
}

func marshalRaw(obj any) (runtime.RawExtension, error) {
	raw, err := json.Marshal(obj)
	if err != nil {
		return runtime.RawExtension{}, err
	}
	return runtime.RawExtension{Raw: raw}, nil
}

func statusFailure(message string) metav1.Status {
	return apierrors.NewBadRequest(message).Status()
}
