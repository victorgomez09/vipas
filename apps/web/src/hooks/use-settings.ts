import { useMutation, useQuery, useQueryClient } from "@tanstack/react-query";
import { toast } from "sonner";
import { api } from "@/lib/api";
import type { DomainVerificationResult } from "@/types/api";

export function useSettings() {
  return useQuery({
    queryKey: ["settings"],
    queryFn: () => api.get<Record<string, string>>("/api/v1/settings"),
  });
}

export function useUpdateSetting() {
  const qc = useQueryClient();
  return useMutation({
    mutationFn: (data: { key: string; value: string }) =>
      api.put("/api/v1/settings", data),
    onSuccess: (res: any) => {
      if (res.warning) toast.warning(res.warning);
      else toast.success("Setting updated");
      qc.invalidateQueries({ queryKey: ["settings"] });
    },
    onError: (err: any) => toast.error(err?.detail || "Update failed"),
  });
}

export function useVerifyDomain() {
  return useMutation({
    mutationFn: (domain: string) =>
      api.get<DomainVerificationResult>(`/api/v1/settings/verify-domain?domain=${domain}`),
  });
}