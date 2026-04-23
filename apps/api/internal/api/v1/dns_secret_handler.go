package v1

import (
	"net/http"

	"github.com/gin-gonic/gin"
	"github.com/victorgomez09/vipas/apps/api/internal/httputil"
	"github.com/victorgomez09/vipas/apps/api/internal/service"
)

type DNSSecretHandler struct {
	svc *service.SettingService
}

func NewDNSSecretHandler(svc *service.SettingService) *DNSSecretHandler {
	return &DNSSecretHandler{svc: svc}
}

// POST /api/v1/settings/dns-secret
// body: { name: string, data: { key1: value1, ... } }
func (h *DNSSecretHandler) Create(c *gin.Context) {
	var in struct {
		Name string            `json:"name" binding:"required"`
		Data map[string]string `json:"data" binding:"required"`
	}
	if err := c.ShouldBindJSON(&in); err != nil {
		httputil.RespondError(c, err)
		return
	}

	ref, err := h.svc.CreateDNSSecret(c.Request.Context(), in.Name, in.Data)
	if err != nil {
		httputil.RespondError(c, err)
		return
	}
	c.JSON(http.StatusOK, gin.H{"ref": ref})
}
