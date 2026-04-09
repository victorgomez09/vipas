package v1

import (
	"context"
	"strconv"

	"github.com/gin-gonic/gin"
	"github.com/victorgomez09/vipas/apps/api/internal/apierr"
	"github.com/victorgomez09/vipas/apps/api/internal/httputil"
	"github.com/victorgomez09/vipas/apps/api/internal/model"
	"github.com/victorgomez09/vipas/apps/api/internal/orchestrator"
	"github.com/victorgomez09/vipas/apps/api/internal/store"
)

type ClusterHandler struct {
	orch  orchestrator.Orchestrator
	store store.Store
}

func NewClusterHandler(orch orchestrator.Orchestrator, s store.Store) *ClusterHandler {
	return &ClusterHandler{orch: orch, store: s}
}

func (h *ClusterHandler) GetNodes(c *gin.Context) {
	nodes, err := h.orch.GetNodes(c.Request.Context())
	if err != nil {
		httputil.RespondError(c, err)
		return
	}
	httputil.RespondList(c, nodes)
}

func (h *ClusterHandler) GetMetrics(c *gin.Context) {
	metrics, err := h.orch.GetClusterMetrics(c.Request.Context())
	if err != nil {
		httputil.RespondError(c, err)
		return
	}
	httputil.RespondOK(c, metrics)
}

func (h *ClusterHandler) GetAllPods(c *gin.Context) {
	pods, err := h.orch.GetAllPods(c.Request.Context())
	if err != nil {
		httputil.RespondError(c, err)
		return
	}
	httputil.RespondList(c, pods)
}

func (h *ClusterHandler) GetEvents(c *gin.Context) {
	limitStr := c.DefaultQuery("limit", "100")
	limit, _ := strconv.Atoi(limitStr)
	if limit <= 0 || limit > 500 {
		limit = 100
	}
	events, err := h.orch.GetClusterEvents(c.Request.Context(), limit)
	if err != nil {
		httputil.RespondError(c, err)
		return
	}
	httputil.RespondList(c, events)
}

func (h *ClusterHandler) GetPVCs(c *gin.Context) {
	pvcs, err := h.orch.GetPVCs(c.Request.Context())
	if err != nil {
		httputil.RespondError(c, err)
		return
	}
	httputil.RespondList(c, pvcs)
}

func (h *ClusterHandler) GetNamespaces(c *gin.Context) {
	namespaces, err := h.orch.GetNamespaces(c.Request.Context())
	if err != nil {
		httputil.RespondError(c, err)
		return
	}
	httputil.RespondList(c, namespaces)
}

func (h *ClusterHandler) GetNodeMetrics(c *gin.Context) {
	metrics, err := h.orch.GetNodeMetrics(c.Request.Context())
	if err != nil {
		httputil.RespondError(c, err)
		return
	}
	httputil.RespondList(c, metrics)
}

func (h *ClusterHandler) GetTopology(c *gin.Context) {
	topo, err := h.orch.GetClusterTopology(c.Request.Context())
	if err != nil {
		httputil.RespondError(c, err)
		return
	}
	httputil.RespondOK(c, topo)
}

func (h *ClusterHandler) GetNodePools(c *gin.Context) {
	pools, err := h.orch.GetNodePools(c.Request.Context())
	if err != nil {
		httputil.RespondError(c, err)
		return
	}
	httputil.RespondOK(c, pools)
}

func (h *ClusterHandler) SetNodePool(c *gin.Context) {
	nodeName := c.Param("name")
	var body struct {
		Pool string `json:"pool"`
	}
	if err := c.ShouldBindJSON(&body); err != nil {
		httputil.RespondError(c, apierr.ErrBadRequest.WithDetail(err.Error()))
		return
	}
	if body.Pool == "" {
		// Remove pool label
		if err := h.orch.RemoveNodeLabel(c.Request.Context(), nodeName, "vipas/pool"); err != nil {
			httputil.RespondError(c, err)
			return
		}
	} else {
		if err := h.orch.SetNodeLabel(c.Request.Context(), nodeName, "vipas/pool", body.Pool); err != nil {
			httputil.RespondError(c, err)
			return
		}
	}
	httputil.RespondOK(c, gin.H{"message": "node pool updated"})
}

func (h *ClusterHandler) GetTraefikConfig(c *gin.Context) {
	httputil.RespondError(c, apierr.ErrNotFound)
}

func (h *ClusterHandler) UpdateTraefikConfig(c *gin.Context) {
	var body struct {
		YAML string `json:"yaml" binding:"required"`
	}
	if err := c.ShouldBindJSON(&body); err != nil {
		httputil.RespondError(c, apierr.ErrBadRequest.WithDetail(err.Error()))
		return
	}
	httputil.RespondError(c, apierr.ErrNotFound)
}

func (h *ClusterHandler) GetHelmReleases(c *gin.Context) {
	releases, err := h.orch.GetHelmReleases(c.Request.Context())
	if err != nil {
		httputil.RespondError(c, err)
		return
	}
	httputil.RespondList(c, releases)
}

func (h *ClusterHandler) GetDaemonSets(c *gin.Context) {
	daemonSets, err := h.orch.GetDaemonSets(c.Request.Context())
	if err != nil {
		httputil.RespondError(c, err)
		return
	}
	httputil.RespondList(c, daemonSets)
}

func (h *ClusterHandler) DeletePVC(c *gin.Context) {
	namespace := c.Param("namespace")
	name := c.Param("name")
	if namespace == "" || name == "" {
		httputil.RespondError(c, apierr.ErrBadRequest.WithDetail("namespace and name are required"))
		return
	}
	if err := h.orch.DeleteVolume(c.Request.Context(), name, namespace); err != nil {
		httputil.RespondError(c, err)
		return
	}
	httputil.RespondOK(c, gin.H{"message": "PVC deleted"})
}

func (h *ClusterHandler) GetCleanupStats(c *gin.Context) {
	ctx := c.Request.Context()
	stats, err := h.orch.GetCleanupStats(ctx)
	if err != nil {
		httputil.RespondError(c, err)
		return
	}

	// Enrich with orphan route detection (requires DB access)
	validHosts := h.getValidDomainHosts(ctx)
	systemRoutes := h.getSystemRoutes(ctx)
	orphans, orphanErr := h.orch.GetOrphanRoutes(ctx, validHosts, systemRoutes)
	if orphanErr == nil && orphans != nil {
		stats.OrphanRoutes = len(orphans)
		stats.OrphanRouteNames = orphans
	}
	if stats.OrphanRouteNames == nil {
		stats.OrphanRouteNames = []string{}
	}

	httputil.RespondOK(c, stats)
}

// GetLBStatus returns load balancer status (IP pools and assigned IPs).
func (h *ClusterHandler) GetLBStatus(c *gin.Context) {
	status, err := h.orch.GetLoadBalancerStatus(c.Request.Context())
	if err != nil {
		httputil.RespondError(c, err)
		return
	}
	httputil.RespondOK(c, status)
}

// getValidDomainHosts returns the set of all app domain hosts in the database.
func (h *ClusterHandler) getValidDomainHosts(ctx context.Context) map[string]bool {
	hosts := make(map[string]bool)
	apps, _, _ := h.store.Applications().ListAll(ctx, store.ListParams{Page: 1, PerPage: 1000}, store.AppListFilter{})
	for _, app := range apps {
		domains, _ := h.store.Domains().ListByApp(ctx, app.ID)
		for _, d := range domains {
			hosts[d.Host] = true
		}
	}
	return hosts
}

// getSystemRoutes returns system-managed routes keyed by "namespace/name"
// with their currently expected host. These are validated by resource identity
// (not the global host list) so that an app route sharing the same host is
// not accidentally exempt from orphan cleanup.
func (h *ClusterHandler) getSystemRoutes(ctx context.Context) map[string]string {
	si := make(map[string]string)
	if panelDomain, _ := h.store.Settings().Get(ctx, model.SettingPanelDomain); panelDomain != "" {
		si["vipas/vipas-panel"] = panelDomain
		si["vipas/vipas-panel-http"] = panelDomain
	}
	return si
}

func (h *ClusterHandler) CleanupEvictedPods(c *gin.Context) {
	result, err := h.orch.CleanupEvictedPods(c.Request.Context())
	if err != nil {
		httputil.RespondError(c, err)
		return
	}
	httputil.RespondOK(c, result)
}

func (h *ClusterHandler) CleanupFailedPods(c *gin.Context) {
	result, err := h.orch.CleanupFailedPods(c.Request.Context())
	if err != nil {
		httputil.RespondError(c, err)
		return
	}
	httputil.RespondOK(c, result)
}

func (h *ClusterHandler) CleanupCompletedPods(c *gin.Context) {
	result, err := h.orch.CleanupCompletedPods(c.Request.Context())
	if err != nil {
		httputil.RespondError(c, err)
		return
	}
	httputil.RespondOK(c, result)
}

func (h *ClusterHandler) CleanupStaleReplicaSets(c *gin.Context) {
	result, err := h.orch.CleanupStaleReplicaSets(c.Request.Context())
	if err != nil {
		httputil.RespondError(c, err)
		return
	}
	httputil.RespondOK(c, result)
}

func (h *ClusterHandler) CleanupCompletedJobs(c *gin.Context) {
	result, err := h.orch.CleanupCompletedJobs(c.Request.Context())
	if err != nil {
		httputil.RespondError(c, err)
		return
	}
	httputil.RespondOK(c, result)
}

func (h *ClusterHandler) CleanupOrphanRoutes(c *gin.Context) {
	ctx := c.Request.Context()
	validHosts := h.getValidDomainHosts(ctx)
	systemRoutes := h.getSystemRoutes(ctx)
	result, err := h.orch.CleanupOrphanRoutes(ctx, validHosts, systemRoutes)
	if err != nil {
		httputil.RespondError(c, err)
		return
	}
	httputil.RespondOK(c, result)
}

func (h *ClusterHandler) ExpandPVC(c *gin.Context) {
	namespace := c.Param("namespace")
	name := c.Param("name")
	if namespace == "" || name == "" {
		httputil.RespondError(c, apierr.ErrBadRequest.WithDetail("namespace and name are required"))
		return
	}
	var body struct {
		Size string `json:"size" binding:"required"`
	}
	if err := c.ShouldBindJSON(&body); err != nil {
		httputil.RespondError(c, apierr.ErrBadRequest.WithDetail(err.Error()))
		return
	}
	if err := h.orch.ExpandVolume(c.Request.Context(), name, namespace, body.Size); err != nil {
		httputil.RespondError(c, err)
		return
	}
	httputil.RespondOK(c, gin.H{"message": "PVC expanded"})
}

func (h *ClusterHandler) RestartTraefik(c *gin.Context) {
	httputil.RespondError(c, apierr.ErrNotFound)
}

func (h *ClusterHandler) GetTraefikStatus(c *gin.Context) {
	httputil.RespondError(c, apierr.ErrNotFound)
}

// GetGatewayStatus ensures the central Gateway exists and reports basic health info.
func (h *ClusterHandler) GetGatewayStatus(c *gin.Context) {
	ctx := c.Request.Context()
	if err := h.orch.EnsureGateway(ctx); err != nil {
		httputil.RespondError(c, err)
		return
	}
	nodes, err := h.orch.GetNodes(ctx)
	if err != nil {
		httputil.RespondError(c, err)
		return
	}
	httputil.RespondOK(c, gin.H{"gateway": "ready", "nodes": len(nodes)})
}

// ListGatewayRoutes returns known domain routes and their HTTPRoute status.
func (h *ClusterHandler) ListGatewayRoutes(c *gin.Context) {
	ctx := c.Request.Context()
	apps, _, _ := h.store.Applications().ListAll(ctx, store.ListParams{Page: 1, PerPage: 1000}, store.AppListFilter{})
	var out []map[string]interface{}
	for _, app := range apps {
		domains, _ := h.store.Domains().ListByApp(ctx, app.ID)
		for _, d := range domains {
			status, _ := h.orch.GetHTTPRouteStatus(ctx, &d, &app)
			m := map[string]interface{}{"app_id": app.ID, "app": app.Name, "host": d.Host, "status": status}
			out = append(out, m)
		}
	}
	httputil.RespondList(c, out)
}
