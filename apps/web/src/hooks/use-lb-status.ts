import { useQuery } from "@tanstack/react-query";

async function fetchLBStatus() {
  const res = await fetch('/api/v1/infra/lb/status');
  if (!res.ok) throw new Error(`fetch lb status failed: ${res.status}`);
  return res.json();
}

export function useLBStatus() {
  const { data, error, isLoading, isFetching } = useQuery({
    queryKey: ['lbStatus'],
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
