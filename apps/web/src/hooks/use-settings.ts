import { useMutation, useQuery, useQueryClient } from "@tanstack/react-query";
import { toast } from "sonner";
import { api } from "@/lib/api";
import type { Settings } from "@/types/api";

export const settingsKeys = {
  all: ["settings"] as const,
};

export function useSettings() {
  return useQuery({
    queryKey: settingsKeys.all,
    queryFn: () => api.get<Settings>("/api/v1/settings"),
  });
}

export interface DomainVerification {
  domain: string;
  dns: "ok" | "failed" | "wrong_ip";
  dns_ip?: string;
  dns_message?: string;
  dns_warning?: string;
  reachable?: boolean;
  reachable_message?: string;
  cert?: "valid" | "self_signed" | "cloudflare" | "none" | "unknown";
  cert_message?: string;
  cert_issuer?: string;
  cert_expiry?: string;
  cert_days?: number;
}

export function useVerifyDomain() {
  return useMutation({
    mutationFn: (domain: string) =>
      api.get<DomainVerification>(
        `/api/v1/settings/verify-domain?domain=${encodeURIComponent(domain)}`,
      ),
  });
}

export function useUpdateSetting() {
  const qc = useQueryClient();
  return useMutation({
    mutationFn: (data: { key: string; value: string }) => api.put("/api/v1/settings", data),
    onSuccess: () => {
      toast.success("Setting updated");
      qc.invalidateQueries({ queryKey: settingsKeys.all });
    },
    onError: (err: any) => toast.error(err?.detail || "Failed to save"),
  });
}
