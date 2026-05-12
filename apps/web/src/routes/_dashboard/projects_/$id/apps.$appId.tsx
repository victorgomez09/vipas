import { createFileRoute, useNavigate } from "@tanstack/react-router";
import { Container, GitBranch, Loader2, RefreshCw, Rocket, Square, Trash, X } from "lucide-react";
import { useState } from "react";
import { ConfirmDialog } from "@/components/confirm-dialog";
import { LoadingScreen } from "@/components/loading-screen";
import { PageHeader } from "@/components/page-header";
import { Badge } from "@/components/ui/badge";
import { Button } from "@/components/ui/button";
import { Separator } from "@/components/ui/separator";
import { Tabs, TabsContent, TabsList, TabsTrigger } from "@/components/ui/tabs";
import {
  useApp,
  useAppDeployments,
  useAppDomains,
  useAppPods,
  useAppStatus,
  useDeleteApp,
  useDeploy,
  useRestartApp,
  useStopApp,
} from "@/hooks/use-apps";
import { statusVariant } from "@/lib/constants";
import { SettingsTab } from "./_components/-advanced-tab";
import { DeploymentsTab } from "./_components/-deployments-tab";
import { DomainsTab } from "./_components/-domains-tab";
import { EnvironmentTab } from "./_components/-environment-tab";
import { GeneralTab } from "./_components/-general-tab";
import { LogsTab } from "./_components/-logs-tab";
import { MonitoringTab } from "./_components/-monitoring-tab";
import { TopologyTab } from "./_components/-topology-tab";
import { VolumesTab } from "./_components/-volumes-tab";

export const Route = createFileRoute("/_dashboard/projects_/$id/apps/$appId")({
  component: AppDetailPage,
});

function AppDetailPage() {
  const { id: projectId, appId } = Route.useParams();
  const navigate = useNavigate();

  // ── Data ────────────────────────────────────────────────────
  const { data: app, isLoading } = useApp(appId);
  const { data: appStatus } = useAppStatus(appId);
  const { data: pods } = useAppPods(appId);
  const { data: deployments } = useAppDeployments(appId);
  const { data: domains } = useAppDomains(appId);

  const safePods = pods ?? [];
  const safeDeployments = deployments ?? [];
  const safeDomains = domains ?? [];

  const activeDeploymentsCount = safeDeployments.filter(
    (d) => d.status === "queued" || d.status === "building" || d.status === "deploying",
  ).length;

  // ── Mutations ───────────────────────────────────────────────
  const deploy = useDeploy(appId);
  const restart = useRestartApp(appId);
  const stop = useStopApp(appId);
  const deleteApp = useDeleteApp(appId);

  // Confirmation dialogs
  const [showDeploy, setShowDeploy] = useState(false);
  const [showRestart, setShowRestart] = useState(false);
  const [showStop, setShowStop] = useState(false);
  const [showDelete, setShowDelete] = useState(false);

  if (isLoading) return <LoadingScreen variant="detail" />;
  if (!app) return null;

  // Always prefer live K8s status; only fall back to DB for stable states
  const liveStatus = appStatus?.phase || app.status;

  const sourceDescription =
    app.source_type === "image" ? (
      <span className="flex items-center gap-1">
        <Container className="h-3 w-3" />
        {app.docker_image}
      </span>
    ) : (
      <span className="flex items-center gap-1">
        <GitBranch className="h-3 w-3" />
        {app.git_repo} @ {app.git_branch}
      </span>
    );

  return (
    <div>
      <PageHeader
        title={app.name}
        description={sourceDescription}
        useBack
        badges={
          <>
            <Badge variant={statusVariant(liveStatus)}>{liveStatus}</Badge>
            <Badge variant="outline" className="text-xs">
              Deployment
            </Badge>
          </>
        }
        actions={
          <>
            {(liveStatus === "running") && (
              <>
                <Button size="sm" variant="outline" onClick={() => setShowRestart(true)}>
                  <RefreshCw className="h-3.5 w-3.5" /> Restart
                </Button>
                <Button size="sm" variant="outline" onClick={() => setShowStop(true)}>
                  <Square className="h-3.5 w-3.5" /> Stop
                </Button>
              </>
            )}
            {(liveStatus === "pending" ||
              liveStatus === "error" ||
              liveStatus === "failed" ||
              liveStatus === "warning" ||
              liveStatus === "CrashLoopBackOff" ||
              liveStatus === "Error" ||
              liveStatus === "unhealthy") && (
                <Button size="sm" variant="destructive" onClick={() => setShowStop(true)}>
                  <Square className="h-3.5 w-3.5" /> Stop
                </Button>
              )}
            <Button
              onClick={() => setShowDeploy(true)}
              disabled={
                deploy.isPending ||
                ["building", "deploying", "restarting", "stopping", "pending"].includes(liveStatus)
              }
            >
              {liveStatus === "pending" || deploy.isPending ? (
                <Loader2 className="h-4 w-4 animate-spin" />
              ) : (
                <Rocket className="h-4 w-4" />
              )}{" "}
              Deploy
            </Button>
          </>
        }
      />
      <Separator className="my-5" />

      <Tabs defaultValue="general" orientation="vertical" className="flex flex-wrap gap-4 w-full h-full">
        <TabsList className="flex-col w-[10em] h-full">
          <TabsTrigger value="general">General</TabsTrigger>
          <TabsTrigger value="topology">Topology</TabsTrigger>
          <TabsTrigger value="domains">Domains</TabsTrigger>
          <TabsTrigger value="environment">Environment</TabsTrigger>
          <TabsTrigger value="deployments" className="gap-1.5">
            Deployments
            {activeDeploymentsCount > 0 && (
              <Badge variant="warning" className="ml-0.5 h-5 px-1.5 text-xs">
                {activeDeploymentsCount}
              </Badge>
            )}
          </TabsTrigger>
          <TabsTrigger value="volumes">Volumes</TabsTrigger>
          <TabsTrigger value="monitoring">Monitoring</TabsTrigger>
          <TabsTrigger value="logs">Logs</TabsTrigger>
          <TabsTrigger value="settings">Settings</TabsTrigger>
        </TabsList>

        <div className="flex-1">
          <TabsContent value="general" className="mt-0 w-full">
            <GeneralTab app={app} appStatus={appStatus} pods={safePods} />
          </TabsContent>

          <TabsContent value="topology" className="space-y-4 mt-0 w-full">
            <TopologyTab app={app} appStatus={appStatus} pods={safePods} domains={safeDomains} />
          </TabsContent>

          <TabsContent value="domains" className="space-y-4 mt-0 w-full">
            <DomainsTab appId={appId} domains={safeDomains} />
          </TabsContent>

          <TabsContent value="environment" className="space-y-4 mt-0 w-full">
            <EnvironmentTab
              key={`${appId}-${JSON.stringify(app.env_vars)}-${JSON.stringify(app.build_env_vars)}`}
              appId={appId}
              envVars={app.env_vars ?? {}}
              buildEnvVars={app.build_env_vars ?? {}}
            />
          </TabsContent>

          <TabsContent value="deployments" className="space-y-4 mt-0 w-full">
            <DeploymentsTab
              app={app}
              appId={appId}
              deployments={safeDeployments}
              deployStrategy={app.deploy_strategy}
            />
          </TabsContent>

          <TabsContent value="volumes" className="space-y-4 mt-0 w-full">
            <VolumesTab app={app} appId={appId} />
          </TabsContent>

          <TabsContent value="monitoring" className="space-y-4 mt-0 w-full">
            <MonitoringTab app={app} appId={appId} />
          </TabsContent>

          <TabsContent value="logs" className="space-y-4 mt-0 w-full">
            <LogsTab appId={appId} appName={app.name} pods={safePods} />
          </TabsContent>

          <TabsContent value="settings" className="space-y-4 mt-0 w-full">
            <SettingsTab app={app} appId={appId} onDelete={() => setShowDelete(true)} />
          </TabsContent>
        </div>
      </Tabs>

      {/* ── Confirmation dialogs ── */}
      <ConfirmDialog
        open={showDeploy}
        onOpenChange={setShowDeploy}
        title="Deploy Application"
        description={
          <>
            Deploy <strong>{app.name}</strong>?{" "}
            {app.source_type === "git"
              ? "If code hasn't changed, the cached image will be reused. Use Force Build to rebuild from scratch."
              : "This will roll out the current image."}
          </>
        }
        confirmLabel="Deploy"
        secondaryLabel={app.source_type === "git" ? "Force Build" : undefined}
        variant="default"
        loading={deploy.isPending}
        onSecondary={
          app.source_type === "git"
            ? () => deploy.mutate({ force_build: true }, { onSuccess: () => setShowDeploy(false) })
            : undefined
        }
        onConfirm={() =>
          deploy.mutate(undefined, {
            onSuccess: () => setShowDeploy(false),
          })
        }
      />

      <ConfirmDialog
        open={showRestart}
        onOpenChange={setShowRestart}
        title="Restart Application"
        description={
          <>
            Restart <strong>{app.name}</strong>? All pods will be recreated. There may be brief
            downtime.
          </>
        }
        confirmLabel="Restart"
        variant="default"
        loading={restart.isPending}
        onConfirm={() =>
          restart.mutate(undefined, {
            onSuccess: () => setShowRestart(false),
          })
        }
      />

      <ConfirmDialog
        open={showStop}
        onOpenChange={setShowStop}
        title="Stop Application"
        description={
          <>
            Stop <strong>{app.name}</strong>? The service will be scaled to zero and become
            unreachable. You can start it again later by deploying.
          </>
        }
        confirmLabel="Stop"
        loading={stop.isPending}
        onConfirm={() =>
          stop.mutate(undefined, {
            onSuccess: () => setShowStop(false),
          })
        }
      />

      <ConfirmDialog
        open={showDelete}
        onOpenChange={setShowDelete}
        title="Delete Application"
        description={
          <>
            Permanently delete <strong>{app.name}</strong> and all K3s resources? This cannot be
            undone.
          </>
        }
        confirmLabel="Delete"
        loading={deleteApp.isPending}
        onConfirm={() =>
          deleteApp.mutate(undefined, {
            onSuccess: () => navigate({ to: "/projects/$id", params: { id: projectId } }),
          })
        }
      />
    </div>
  );
}
