package main

import (
	"context"
	"fmt"
	"net/http"
	"os"
	"time"

	corev1 "k8s.io/api/core/v1"
	"k8s.io/apimachinery/pkg/types"
	"k8s.io/client-go/kubernetes"
	metricsv "k8s.io/metrics/pkg/client/clientset/versioned"
	ctrl "sigs.k8s.io/controller-runtime"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/log/zap"

	"github.com/zufardhiyaulhaq/kato/api/v1alpha1"
	"github.com/zufardhiyaulhaq/kato/internal/config"
	"github.com/zufardhiyaulhaq/kato/internal/controller"
	"github.com/zufardhiyaulhaq/kato/internal/engine"
	"github.com/zufardhiyaulhaq/kato/internal/methods"
	"github.com/zufardhiyaulhaq/kato/internal/server"
	"github.com/zufardhiyaulhaq/kato/internal/store"
	"github.com/zufardhiyaulhaq/kato/internal/summarizer"
)

func main() {
	if err := run(); err != nil {
		fmt.Fprintln(os.Stderr, "fatal:", err)
		os.Exit(1)
	}
}

func run() error {
	ctrl.SetLogger(zap.New(zap.UseDevMode(true)))
	cfg := config.Load()
	log := ctrl.Log.WithName("kato")

	restCfg := ctrl.GetConfigOrDie()
	mgr, err := ctrl.NewManager(restCfg, ctrl.Options{})
	if err != nil {
		return fmt.Errorf("create manager: %w", err)
	}
	if err := v1alpha1.AddToScheme(mgr.GetScheme()); err != nil {
		return fmt.Errorf("add scheme: %w", err)
	}

	// Typed Kubernetes client for methods (read-only cluster access).
	kubeClient, err := kubernetes.NewForConfig(restCfg)
	if err != nil {
		return fmt.Errorf("kube client: %w", err)
	}

	// Metrics client for live pod CPU/memory usage. Constructing it never fails
	// when metrics-server is absent; methods that use it report metrics as
	// unavailable at call time rather than erroring.
	metricsClient, err := metricsv.NewForConfig(restCfg)
	if err != nil {
		return fmt.Errorf("metrics client: %w", err)
	}

	// Uncached client for namespace-scoped operations: Run CRs and ModelConfig
	// API-key Secrets, both in kato's own namespace. The manager's cache-backed
	// client (mgr.GetClient) would set up cluster-wide LIST+WATCH informers for
	// these types, which the read-only namespaced RBAC denies (and would cache
	// every Secret in the cluster). Point gets and namespaced lists via an
	// uncached client match the granted permissions exactly. Reconcilers below
	// keep the cached client for the cluster-scoped UseCase/ModelConfig watches
	// the ClusterRole permits.
	directClient, err := client.New(restCfg, client.Options{Scheme: mgr.GetScheme()})
	if err != nil {
		return fmt.Errorf("direct client: %w", err)
	}

	reg := methods.Builtin()
	ucCache := controller.NewUseCaseCache()
	mcCache := controller.NewModelConfigCache()
	mcCache.APIKeyLookup = func(ctx context.Context, name, key string) (string, error) {
		var sec corev1.Secret
		if err := directClient.Get(ctx, types.NamespacedName{Namespace: cfg.Namespace, Name: name}, &sec); err != nil {
			return "", err
		}
		v, ok := sec.Data[key]
		if !ok {
			return "", fmt.Errorf("secret %s has no key %q", name, key)
		}
		return string(v), nil
	}

	if err := (&controller.UseCaseReconciler{
		Client: mgr.GetClient(), Cache: ucCache, Registry: reg, ModelConfigs: mcCache,
	}).SetupWithManager(mgr); err != nil {
		return fmt.Errorf("setup usecase controller: %w", err)
	}
	if err := (&controller.ModelConfigReconciler{
		Client: mgr.GetClient(), Cache: mcCache,
	}).SetupWithManager(mgr); err != nil {
		return fmt.Errorf("setup modelconfig controller: %w", err)
	}

	sum := &summarizer.Summarizer{Resolve: mcCache.Resolve}
	eng := &engine.Engine{
		Deps:      methods.Deps{Kube: kubeClient, Metrics: metricsClient},
		Registry:  reg,
		Summarize: sum.Summarize, StepTimeout: cfg.StepTimeout,
	}
	if err := (&controller.RunReconciler{
		Client:      mgr.GetClient(),
		UseCases:    ucCache,
		Execute:     eng.Execute,
		Concurrency: cfg.RunReconcileConcurrency,
	}).SetupWithManager(mgr); err != nil {
		return fmt.Errorf("setup run controller: %w", err)
	}

	st := &store.Store{Client: directClient, Namespace: cfg.Namespace, TTL: cfg.RunTTL}

	srv := &server.Server{
		UseCases: ucCache, Runs: st, Execute: eng.Execute,
		Registry: reg, MaxConcurrent: cfg.MaxConcurrent,
	}
	httpServer := &http.Server{Addr: cfg.ListenAddr, Handler: srv.Handler()}

	// Run HTTP server and GC loop as manager Runnables so they share its
	// lifecycle/signals.
	if err := mgr.Add(httpRunnable{httpServer, log}); err != nil {
		return err
	}
	if err := mgr.Add(gcRunnable{store: st, interval: cfg.GCInterval, maxDuration: cfg.RunMaxDuration, log: log}); err != nil {
		return err
	}

	log.Info("starting kato", "addr", cfg.ListenAddr, "namespace", cfg.Namespace)
	return mgr.Start(ctrl.SetupSignalHandler())
}

type httpRunnable struct {
	srv *http.Server
	log interface{ Info(string, ...any) }
}

func (h httpRunnable) Start(ctx context.Context) error {
	go func() { <-ctx.Done(); _ = h.srv.Shutdown(context.Background()) }()
	if err := h.srv.ListenAndServe(); err != nil && err != http.ErrServerClosed {
		return err
	}
	return nil
}

type gcRunnable struct {
	store       *store.Store
	interval    time.Duration
	maxDuration time.Duration
	log         interface{ Info(string, ...any) }
}

func (g gcRunnable) Start(ctx context.Context) error {
	t := time.NewTicker(g.interval)
	defer t.Stop()
	for {
		select {
		case <-ctx.Done():
			return nil
		case <-t.C:
			n, err := g.store.GarbageCollect(ctx, time.Now())
			if err != nil {
				g.log.Info("run GC error", "err", err.Error())
			} else if n > 0 {
				g.log.Info("garbage-collected runs", "count", n)
			}
			reaped, err := g.store.ReapStuckRuns(ctx, time.Now(), g.maxDuration)
			if err != nil {
				g.log.Info("run reap error", "err", err.Error())
			} else if reaped > 0 {
				g.log.Info("reaped stuck runs", "count", reaped)
			}
		}
	}
}
