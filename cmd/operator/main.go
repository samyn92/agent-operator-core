package main

import (
	"flag"
	"net/http"
	"os"

	"k8s.io/apimachinery/pkg/runtime"
	utilruntime "k8s.io/apimachinery/pkg/util/runtime"
	clientgoscheme "k8s.io/client-go/kubernetes/scheme"
	ctrl "sigs.k8s.io/controller-runtime"
	"sigs.k8s.io/controller-runtime/pkg/cache"
	"sigs.k8s.io/controller-runtime/pkg/healthz"
	"sigs.k8s.io/controller-runtime/pkg/log/zap"
	metricsserver "sigs.k8s.io/controller-runtime/pkg/metrics/server"

	agentsv1alpha1 "github.com/samyn92/agent-operator-core/api/v1alpha1"
	"github.com/samyn92/agent-operator-core/internal/controller"
	"github.com/samyn92/agent-operator-core/internal/forge"
	"github.com/samyn92/agent-operator-core/internal/webhook"
)

var (
	scheme   = runtime.NewScheme()
	setupLog = ctrl.Log.WithName("setup")
)

func init() {
	utilruntime.Must(clientgoscheme.AddToScheme(scheme))
	utilruntime.Must(agentsv1alpha1.AddToScheme(scheme))
}

func main() {
	var metricsAddr string
	var probeAddr string
	var webhookAddr string
	var callbackBaseURL string
	var watchNamespace string
	var enableLeaderElection bool

	flag.StringVar(&metricsAddr, "metrics-bind-address", ":8080", "The address the metric endpoint binds to.")
	flag.StringVar(&probeAddr, "health-probe-bind-address", ":8081", "The address the probe endpoint binds to.")
	flag.StringVar(&webhookAddr, "webhook-bind-address", ":9090", "The address the webhook server binds to.")
	flag.StringVar(&callbackBaseURL, "callback-base-url", "", "Base URL for pi-runner callback events (e.g. http://agent-operator-webhook.agents.svc.cluster.local:9090/callback). If empty, tracing is disabled.")
	flag.StringVar(&watchNamespace, "namespace", "", "The namespace to watch. If empty, watches all namespaces.")
	flag.BoolVar(&enableLeaderElection, "leader-elect", false, "Enable leader election for controller manager.")

	opts := zap.Options{Development: true}
	opts.BindFlags(flag.CommandLine)
	flag.Parse()

	ctrl.SetLogger(zap.New(zap.UseFlagOptions(&opts)))

	mgrOptions := ctrl.Options{
		Scheme:                 scheme,
		Metrics:                metricsserver.Options{BindAddress: metricsAddr},
		HealthProbeBindAddress: probeAddr,
		LeaderElection:         enableLeaderElection,
		LeaderElectionID:       "agent-operator.agents.io",
	}

	if watchNamespace != "" {
		setupLog.Info("restricting cache to namespace", "namespace", watchNamespace)
		mgrOptions.Cache = cache.Options{
			DefaultNamespaces: map[string]cache.Config{
				watchNamespace: {},
			},
		}
	}

	mgr, err := ctrl.NewManager(ctrl.GetConfigOrDie(), mgrOptions)
	if err != nil {
		setupLog.Error(err, "unable to start manager")
		os.Exit(1)
	}

	if err = (&controller.AgentReconciler{
		Client: mgr.GetClient(),
		Scheme: mgr.GetScheme(),
	}).SetupWithManager(mgr); err != nil {
		setupLog.Error(err, "unable to create controller", "controller", "Agent")
		os.Exit(1)
	}

	if err = (&controller.WorkflowReconciler{
		Client: mgr.GetClient(),
		Scheme: mgr.GetScheme(),
	}).SetupWithManager(mgr); err != nil {
		setupLog.Error(err, "unable to create controller", "controller", "Workflow")
		os.Exit(1)
	}

	if err = (&controller.WorkflowRunReconciler{
		Client:          mgr.GetClient(),
		Scheme:          mgr.GetScheme(),
		CallbackBaseURL: callbackBaseURL,
	}).SetupWithManager(mgr); err != nil {
		setupLog.Error(err, "unable to create controller", "controller", "WorkflowRun")
		os.Exit(1)
	}

	if err = (&controller.ChannelReconciler{
		Client: mgr.GetClient(),
		Scheme: mgr.GetScheme(),
	}).SetupWithManager(mgr); err != nil {
		setupLog.Error(err, "unable to create controller", "controller", "Channel")
		os.Exit(1)
	}

	if err = (&controller.CapabilityReconciler{
		Client: mgr.GetClient(),
		Scheme: mgr.GetScheme(),
	}).SetupWithManager(mgr); err != nil {
		setupLog.Error(err, "unable to create controller", "controller", "Capability")
		os.Exit(1)
	}

	if err = (&controller.PiAgentReconciler{
		Client: mgr.GetClient(),
		Scheme: mgr.GetScheme(),
	}).SetupWithManager(mgr); err != nil {
		setupLog.Error(err, "unable to create controller", "controller", "PiAgent")
		os.Exit(1)
	}

	if err = (&controller.GitRepoReconciler{
		Client:       mgr.GetClient(),
		Scheme:       mgr.GetScheme(),
		GitHubClient: forge.NewGitHubClient(),
		GitLabClient: forge.NewGitLabClient(),
	}).SetupWithManager(mgr); err != nil {
		setupLog.Error(err, "unable to create controller", "controller", "GitRepo")
		os.Exit(1)
	}

	if err = (&controller.GitWorkspaceReconciler{
		Client: mgr.GetClient(),
		Scheme: mgr.GetScheme(),
	}).SetupWithManager(mgr); err != nil {
		setupLog.Error(err, "unable to create controller", "controller", "GitWorkspace")
		os.Exit(1)
	}

	if err := mgr.AddHealthzCheck("healthz", healthz.Ping); err != nil {
		setupLog.Error(err, "unable to set up health check")
		os.Exit(1)
	}
	if err := mgr.AddReadyzCheck("readyz", healthz.Ping); err != nil {
		setupLog.Error(err, "unable to set up ready check")
		os.Exit(1)
	}

	// Start webhook server if address is specified
	if webhookAddr != "" && webhookAddr != "0" {
		webhookServer := webhook.NewServer(mgr.GetClient(), "")
		callbackHandler := webhook.NewCallbackHandler(mgr.GetClient())
		go func() {
			setupLog.Info("starting webhook server", "addr", webhookAddr)
			mux := http.NewServeMux()
			mux.Handle("/webhook/", http.StripPrefix("/webhook", webhookServer))
			mux.Handle("/webhook", webhookServer)
			mux.Handle("/callback/", http.StripPrefix("/callback", callbackHandler))
			mux.HandleFunc("/healthz", func(w http.ResponseWriter, r *http.Request) {
				w.WriteHeader(http.StatusOK)
			})
			server := &http.Server{
				Addr:    webhookAddr,
				Handler: mux,
			}
			if err := server.ListenAndServe(); err != nil && err != http.ErrServerClosed {
				setupLog.Error(err, "webhook server error")
			}
		}()
	}

	setupLog.Info("starting manager")
	if err := mgr.Start(ctrl.SetupSignalHandler()); err != nil {
		setupLog.Error(err, "problem running manager")
		os.Exit(1)
	}
}
