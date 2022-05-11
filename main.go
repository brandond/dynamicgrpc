package main

import (
	"crypto"
	"crypto/tls"
	"crypto/x509"
	"net/http"
	"strings"
	"time"

	"github.com/rancher/dynamiclistener"
	"github.com/rancher/dynamiclistener/server"
	"github.com/rancher/dynamiclistener/storage/kubernetes"
	"github.com/rancher/wrangler/pkg/generated/controllers/core"
	"github.com/rancher/wrangler/pkg/signals"
	"github.com/rancher/wrangler/pkg/start"
	"github.com/sirupsen/logrus"
	"google.golang.org/grpc"
	"google.golang.org/grpc/health"
	healthpb "google.golang.org/grpc/health/grpc_health_v1"
	"google.golang.org/grpc/keepalive"
	"k8s.io/client-go/tools/clientcmd"
)

func main() {
	logrus.SetLevel(logrus.DebugLevel)

	handler := http.NewServeMux()
	ctx := signals.SetupSignalContext()

	loadingRules := clientcmd.NewDefaultClientConfigLoadingRules()
	restConfig, err := clientcmd.NewNonInteractiveDeferredLoadingClientConfig(loadingRules, &clientcmd.ConfigOverrides{}).ClientConfig()
	if err != nil {
		logrus.Panic(err)
	}

	coreGetter, err := core.NewFactoryFromConfig(restConfig)
	if err != nil {
		logrus.Panic(err)
	}

	secrets := coreGetter.Core().V1().Secret()

	caCert, caKey, err := kubernetes.LoadOrGenCA(secrets, "default", "dynamiclister-ca")
	if err != nil {
		logrus.Panic(err)
	}

	opts := &server.ListenOpts{
		Secrets:       secrets,
		CA:            caCert,
		CAKey:         caKey,
		CAName:        "dynamiclistener-ca",
		CANamespace:   "default",
		CertName:      "dynamiclistener",
		CertNamespace: "default",
		TLSListenerConfig: dynamiclistener.Config{
			TLSConfig: &tls.Config{
				ClientAuth: tls.RequestClientCert,
				NextProtos: []string{"h2", "http/1.1"},
			},
		},
	}

	grpcServer, err := grpcServer(caCert, caKey)
	if err != nil {
		logrus.Panic(err)
	}

	hsrv := health.NewServer()
	hsrv.SetServingStatus("ok", healthpb.HealthCheckResponse_SERVING)
	healthpb.RegisterHealthServer(grpcServer, hsrv)

	if err := server.ListenAndServe(ctx, 8443, 8080, grpcHandlerFunc(grpcServer, handler), opts); err != nil {
		logrus.Panic(err)
	}

	if err := start.All(ctx, 5, coreGetter); err != nil {
		logrus.Panic(err)
	}

	<-ctx.Done()
}

func grpcServer(cert *x509.Certificate, key crypto.Signer) (*grpc.Server, error) {
	gopts := []grpc.ServerOption{
		grpc.KeepaliveEnforcementPolicy(keepalive.EnforcementPolicy{
			MinTime:             5 * time.Second,
			PermitWithoutStream: false,
		}),
		grpc.KeepaliveParams(keepalive.ServerParameters{
			Time:    2 * time.Hour,
			Timeout: 20 * time.Second,
		}),
	}

	//creds := credentials.NewServerTLSFromCert(&tls.Certificate{
	//	Certificate: [][]byte{cert.Raw},
	//	PrivateKey:  key,
	//})

	//gopts = append(gopts, grpc.Creds(creds))
	return grpc.NewServer(gopts...), nil
}

func grpcHandlerFunc(grpcServer *grpc.Server, httpHandler http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		logrus.Infof("grpcHandlerFunc handling %s request for %s with content-type %s", r.Proto, r.URL, r.Header.Get("Content-Type"))
		if r.ProtoMajor == 2 && strings.HasPrefix(r.Header.Get("Content-Type"), "application/grpc") {
			grpcServer.ServeHTTP(w, r)
		} else {
			httpHandler.ServeHTTP(w, r)
		}
	})
}
