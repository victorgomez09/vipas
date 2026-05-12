import { useQuery } from "@tanstack/react-query";
import { api } from "@/lib/api";

export interface LBStatus {
  type: string;
  ip_pools: string[];
  assigned_ips: string[];
  bgp_peers: { name: string; peer_address: string; peer_asn: number }[];
}

async function fetchLBStatus(): Promise<LBStatus> {
  return api.get<LBStatus>("/api/v1/infra/lb/status");
}

export function useLBStatus() {
  const { data, error, isLoading, isFetching } = useQuery({
    queryKey: ["lbStatus"],
    queryFn: fetchLBStatus,
    refetchInterval: 5000,
  });

  return {
    status: data,
    loading: isLoading,
    error,
    validating: isFetching,
  };
}

export default useLBStatus;
