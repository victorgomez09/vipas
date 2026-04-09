package main

import (
	"context"
	"fmt"
	"log/slog"
	"os"
	"time"

	"github.com/uptrace/bun/migrate"

	"github.com/victorgomez09/vipas/apps/api/internal/config"
	"github.com/victorgomez09/vipas/apps/api/internal/store/pg"
	"github.com/victorgomez09/vipas/apps/api/migrations"

	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"k8s.io/apimachinery/pkg/runtime/schema"
	"k8s.io/client-go/dynamic"
	"k8s.io/client-go/rest"
	"k8s.io/client-go/tools/clientcmd"
)

func main() {
	logger := slog.New(slog.NewJSONHandler(os.Stdout, &slog.HandlerOptions{Level: slog.LevelInfo}))

	if len(os.Args) < 2 {
		fmt.Println("Usage: migrate <command> [args]")
		fmt.Println("Commands: up, rollback, create <name>, status, init")
		os.Exit(1)
	}

	cfg, err := config.Load()
	if err != nil {
		logger.Error("failed to load config", slog.Any("error", err))
		os.Exit(1)
	}

	store, err := pg.New(cfg.Database.URL)
	if err != nil {
		logger.Error("failed to connect to database", slog.Any("error", err))
		os.Exit(1)
	}
	defer func() { _ = store.Close() }()

	migrator := migrate.NewMigrator(store.DB(), migrations.Migrations)
	ctx := context.Background()

	command := os.Args[1]

	switch command {
	case "init":
		if err := migrator.Init(ctx); err != nil {
			logger.Error("init failed", slog.Any("error", err))
			os.Exit(1)
		}
		fmt.Println("Migration tables created.")

	case "up":
		if err := migrator.Init(ctx); err != nil {
			logger.Error("init failed", slog.Any("error", err))
			os.Exit(1)
		}
		group, err := migrator.Migrate(ctx)
		if err != nil {
			logger.Error("migration failed", slog.Any("error", err))
			os.Exit(1)
		}
		if group.IsZero() {
			fmt.Println("No new migrations to run.")
		} else {
			fmt.Printf("Migrated: %s\n", group)
		}

	case "rollback":
		group, err := migrator.Rollback(ctx)
		if err != nil {
			logger.Error("rollback failed", slog.Any("error", err))
			os.Exit(1)
		}
		if group.IsZero() {
			fmt.Println("Nothing to rollback.")
		} else {
			fmt.Printf("Rolled back: %s\n", group)
		}

	case "status":
		if err := migrator.Init(ctx); err != nil {
			logger.Error("init failed", slog.Any("error", err))
			os.Exit(1)
		}
		ms, err := migrator.MigrationsWithStatus(ctx)
		if err != nil {
			logger.Error("status failed", slog.Any("error", err))
			os.Exit(1)
		}
		fmt.Printf("migrations: %s\n", ms)
		fmt.Printf("unapplied: %s\n", ms.Unapplied())
		fmt.Printf("last group: %s\n", ms.LastGroup())

	case "create":
		if len(os.Args) < 3 {
			fmt.Println("Usage: migrate create <name>")
			os.Exit(1)
		}
		name := os.Args[2]
		mf, err := migrator.CreateGoMigration(ctx, name)
		if err != nil {
			logger.Error("create failed", slog.Any("error", err))
			os.Exit(1)
		}
		fmt.Printf("Created migration: %s\n", mf.Name)

	case "ingress-to-httproute":
		// Migrate vipas-managed Ingress -> HTTPRoute
		if err := migrateIngresses(ctx); err != nil {
			logger.Error("migration failed", slog.Any("error", err))
			os.Exit(1)
		}
		fmt.Println("ingress-to-httproute: migration completed")

	default:
		fmt.Printf("Unknown command: %s\n", command)
		os.Exit(1)
	}
}

// migrateIngresses performs a best-effort migration of Ingress resources labeled
// "app.kubernetes.io/managed-by=vipas" into Gateway API HTTPRoute resources
// pointing to the same backend service. It waits for the HTTPRoute to be
// Accepted before deleting the original Ingress to avoid downtime.
func migrateIngresses(ctx context.Context) error {
	// Build kubeconfig (KUBECONFIG env or in-cluster)
	kubeconfig := os.Getenv("KUBECONFIG")
	var cfg *rest.Config
	var err error
	if kubeconfig != "" {
		cfg, err = clientcmd.BuildConfigFromFlags("", kubeconfig)
		if err != nil {
			return fmt.Errorf("build kubeconfig: %w", err)
		}
	} else {
		// Try in-cluster
		cfg, err = rest.InClusterConfig()
		if err != nil {
			// fallback to default loading rules
			cfg, err = clientcmd.BuildConfigFromFlags("", "")
			if err != nil {
				return fmt.Errorf("cannot load kubeconfig: %w", err)
			}
		}
	}

	dyn, err := dynamic.NewForConfig(cfg)
	if err != nil {
		return fmt.Errorf("create dynamic client: %w", err)
	}

	ingressGVR := schema.GroupVersionResource{Group: "networking.k8s.io", Version: "v1", Resource: "ingresses"}
	httpRouteGVR := schema.GroupVersionResource{Group: "gateway.networking.k8s.io", Version: "v1", Resource: "httproutes"}

	// List Ingresses labeled managed-by=vipas across all namespaces
	list, err := dyn.Resource(ingressGVR).List(ctx, metav1.ListOptions{LabelSelector: "app.kubernetes.io/managed-by=vipas"})
	if err != nil {
		return fmt.Errorf("list ingresses: %w", err)
	}

	for _, ing := range list.Items {
		ns := ing.GetNamespace()
		name := ing.GetName()
		fmt.Printf("migrating ingress %s/%s\n", ns, name)

		// Extract host and backend service from the first rule/path available
		rules, _, _ := unstructured.NestedSlice(ing.Object, "spec", "rules")
		var host string
		var svcName string
		var svcPort interface{}
		if len(rules) > 0 {
			if rmap, ok := rules[0].(map[string]interface{}); ok {
				if h, ok := rmap["host"].(string); ok {
					host = h
				}
				// attempt to read first path backend
				if httpMap, ok := rmap["http"].(map[string]interface{}); ok {
					if paths, ok := httpMap["paths"].([]interface{}); ok && len(paths) > 0 {
						if p0, ok := paths[0].(map[string]interface{}); ok {
							if backend, ok := p0["backend"].(map[string]interface{}); ok {
								if svc, ok := backend["service"].(map[string]interface{}); ok {
									if n, ok := svc["name"].(string); ok {
										svcName = n
									}
									if port, ok := svc["port"]; ok {
										svcPort = port
									}
								}
							}
						}
					}
				}
			}
		}
		if host == "" {
			// fallback to default host if present in spec.rules
			host = ""
		}
		// Build HTTPRoute name from ingress name
		hrName := name + "-httproute"
		if len(hrName) > 63 {
			hrName = hrName[:63]
		}

		// Determine backend port number if possible
		backendPort := int64(80)
		if svcPort != nil {
			if m, ok := svcPort.(map[string]interface{}); ok {
				if num, ok := m["number"].(int64); ok {
					backendPort = num
				} else if f, ok := m["number"].(float64); ok {
					backendPort = int64(f)
				}
			}
		}

		// Build HTTPRoute unstructured
		hr := &unstructured.Unstructured{Object: map[string]interface{}{
			"apiVersion": "gateway.networking.k8s.io/v1",
			"kind":       "HTTPRoute",
			"metadata": map[string]interface{}{
				"name":      hrName,
				"namespace": ns,
				"labels": map[string]interface{}{
					"app.kubernetes.io/managed-by": "vipas",
				},
			},
			"spec": map[string]interface{}{
				"parentRefs": []interface{}{map[string]interface{}{"name": "vipas-gateway", "namespace": "gateway-system", "sectionName": "http"}},
				"hostnames":  []interface{}{host},
				"rules": []interface{}{map[string]interface{}{
					"matches":     []interface{}{map[string]interface{}{"path": map[string]interface{}{"type": "PathPrefix", "value": "/"}}},
					"backendRefs": []interface{}{map[string]interface{}{"name": svcName, "port": backendPort, "weight": int64(1)}},
				}},
			},
		}}

		// Create or Update HTTPRoute
		if _, err := dyn.Resource(httpRouteGVR).Namespace(ns).Get(ctx, hrName, metav1.GetOptions{}); err != nil {
			if _, cErr := dyn.Resource(httpRouteGVR).Namespace(ns).Create(ctx, hr, metav1.CreateOptions{}); cErr != nil {
				fmt.Printf("failed to create HTTPRoute %s/%s: %v\n", ns, hrName, cErr)
				continue
			}
			fmt.Printf("created httproute %s/%s\n", ns, hrName)
		} else {
			if _, uErr := dyn.Resource(httpRouteGVR).Namespace(ns).Update(ctx, hr, metav1.UpdateOptions{}); uErr != nil {
				fmt.Printf("failed to update HTTPRoute %s/%s: %v\n", ns, hrName, uErr)
				continue
			}
			fmt.Printf("updated httproute %s/%s\n", ns, hrName)
		}

		// Wait for Accepted condition
		accepted := false
		timeout := time.After(2 * time.Minute)
		ticker := time.NewTicker(2 * time.Second)
		defer ticker.Stop()
		for !accepted {
			select {
			case <-timeout:
				fmt.Printf("timeout waiting for HTTPRoute %s/%s Accepted\n", ns, hrName)
				accepted = false
				break
			case <-ticker.C:
				obj, gErr := dyn.Resource(httpRouteGVR).Namespace(ns).Get(ctx, hrName, metav1.GetOptions{})
				if gErr != nil {
					continue
				}
				parents, found, _ := unstructured.NestedSlice(obj.Object, "status", "parents")
				if found {
					for _, p := range parents {
						if pm, ok := p.(map[string]interface{}); ok {
							if conds, ok := pm["conditions"].([]interface{}); ok {
								for _, c := range conds {
									if cm, ok := c.(map[string]interface{}); ok {
										if t, _ := cm["type"].(string); t == "Accepted" {
											if s, _ := cm["status"].(string); s == "True" {
												accepted = true
												break
											}
										}
									}
								}
							}
						}
						if accepted {
							break
						}
					}
				}
			}
			if accepted {
				break
			}
		}

		if accepted {
			// Delete original ingress
			if err := dyn.Resource(ingressGVR).Namespace(ns).Delete(ctx, name, metav1.DeleteOptions{}); err != nil {
				fmt.Printf("failed to delete ingress %s/%s: %v\n", ns, name, err)
			} else {
				fmt.Printf("deleted ingress %s/%s after successful migration\n", ns, name)
			}
		} else {
			fmt.Printf("skipping deletion of ingress %s/%s due to missing Accepted status\n", ns, name)
		}
	}

	return nil
}
