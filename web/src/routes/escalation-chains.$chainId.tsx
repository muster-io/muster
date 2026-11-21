// SPDX-License-Identifier: AGPL-3.0-only
// Copyright The Muster Authors

// An Escalation chain (C-32.FR-1, FR-2, FR-3, FR-17, FR-18; C-32.AC-14): where it writes and when it stops, "Run for",
// the warnings, the steps with their limits as chips, and "How it went" with the recent runs. Admins
// (escalation-chains:write) edit and delete it; everyone else reads it. The page keeps the version of the chain it was
// read at and sends it as If-Match with each save, so a save over a newer version shows a conflict.

import { useMutation, useQueryClient } from "@tanstack/react-query";
import { Link, createFileRoute, useNavigate } from "@tanstack/react-router";
import { ArrowLeftIcon } from "lucide-react";
import { useState } from "react";
import { useTranslation } from "react-i18next";

import {
  deleteEscalationChain,
  getGetEscalationChainQueryKey,
  getListEscalationChainsQueryKey,
  updateEscalationChain,
  useGetEscalationChain,
} from "../api/gen/endpoints/escalation-chains/escalation-chains";
import { getListTeamsQueryKey } from "../api/gen/endpoints/teams/teams";
import type { EscalationChain } from "../api/gen/model";
import { RequirePermission, useCan } from "../components/app-shell";
import { PageTitle } from "../components/docs-link";
import { EscalationChainForm } from "../components/escalation-chain-form";
import { EscalationChainRuns } from "../components/escalation-chain-runs";
import { ManagedByBadge, ManagedByDeleteLine, ManagedByWarning } from "../components/managed-by";
import { Alert, AlertDescription } from "../components/ui/alert";
import { Button, buttonVariants } from "../components/ui/button";
import {
  Dialog,
  DialogClose,
  DialogContent,
  DialogDescription,
  DialogFooter,
  DialogHeader,
  DialogTitle,
} from "../components/ui/dialog";
import { isApiError, isStale, problemText } from "../lib/api";

export const Route = createFileRoute("/escalation-chains/$chainId")({
  staticData: { shell: true },
  component: ChainPage,
});

/** "Delete chain": a chain that a Route or a Team names is refused with `chain_in_use`, and the dialog says so. */
function DeleteChain({ chain }: { chain: EscalationChain }) {
  const { t } = useTranslation();
  const queryClient = useQueryClient();
  const navigate = useNavigate();
  const [open, setOpen] = useState(false);
  const remove = useMutation({
    mutationFn: () => deleteEscalationChain(chain.id, { headers: { "If-Match": chain.etag } }),
    onSuccess: async () => {
      await navigate({ to: "/escalation-chains" });
      queryClient.removeQueries({ queryKey: getGetEscalationChainQueryKey(chain.id) });
      void queryClient.invalidateQueries({ queryKey: getListEscalationChainsQueryKey() });
      void queryClient.invalidateQueries({ queryKey: getListTeamsQueryKey() });
    },
  });
  const inUse = isApiError(remove.error) && remove.error.code === "chain_in_use";
  return (
    <>
      <div>
        <Button
          type="button"
          variant="destructive"
          onClick={() => {
            remove.reset();
            setOpen(true);
          }}
        >
          {t("escalation.delete.action")}
        </Button>
      </div>
      <Dialog open={open} onOpenChange={setOpen}>
        <DialogContent closeLabel={t("common.close")}>
          <DialogHeader>
            <DialogTitle className="pr-8 break-words">
              {t("escalation.delete.title", { name: chain.name })}
            </DialogTitle>
            <DialogDescription>{t("escalation.delete.description")}</DialogDescription>
            <ManagedByDeleteLine tool={chain.managed_by} />
          </DialogHeader>
          {remove.isError && (
            <Alert variant="destructive">
              <AlertDescription className="text-current">
                {inUse
                  ? t("escalation.delete.inUse")
                  : isStale(remove.error)
                    ? t("escalation.errors.stale")
                    : problemText(t, remove.error)}
              </AlertDescription>
            </Alert>
          )}
          <DialogFooter>
            <DialogClose render={<Button variant="outline" />}>{t("common.cancel")}</DialogClose>
            <Button
              variant="destructive"
              disabled={remove.isPending}
              onClick={() => remove.mutate()}
            >
              {t("escalation.delete.confirm")}
            </Button>
          </DialogFooter>
        </DialogContent>
      </Dialog>
    </>
  );
}

/** The page of a chain over the version it was read at; a newer version is told, never swapped in unasked. */
function ChainBody({ chain, reload }: { chain: EscalationChain; reload: () => void }) {
  const { t } = useTranslation();
  const queryClient = useQueryClient();
  const canWrite = useCan("escalation-chains:write");
  const [base, setBase] = useState(chain);
  // The versions this page replaced with its own saves: the read may still bring one of them for a moment.
  const [superseded, setSuperseded] = useState<ReadonlySet<string>>(new Set());
  const stale = chain.etag !== base.etag && !superseded.has(chain.etag);
  return (
    <div className="flex min-w-0 flex-col gap-8">
      <ManagedByWarning object="escalationChain" tool={base.managed_by} />
      <EscalationChainForm
        chain={base}
        readOnly={!canWrite}
        submitLabel={t("common.save")}
        stale={stale}
        onReload={reload}
        save={async (input) => {
          const updated = await updateEscalationChain(base.id, input, {
            headers: { "If-Match": base.etag },
          });
          queryClient.setQueryData(getGetEscalationChainQueryKey(base.id), updated);
          void queryClient.invalidateQueries({ queryKey: getListEscalationChainsQueryKey() });
          void queryClient.invalidateQueries({ queryKey: getListTeamsQueryKey() });
          setSuperseded((now) => new Set(now).add(base.etag));
          setBase(updated);
          return updated;
        }}
      />
      <EscalationChainRuns chainId={base.id} steps={base.steps} destinations={base.destinations} />
      {canWrite && <DeleteChain chain={base} />}
    </div>
  );
}

function ChainView({ chainId }: { chainId: string }) {
  const { t } = useTranslation();
  const query = useGetEscalationChain(chainId);
  const chain = query.data;
  const [generation, setGeneration] = useState(0);
  const [reloadingFrom, setReloadingFrom] = useState<number>();
  const { dataUpdatedAt, isFetching, refetch } = query;
  if (reloadingFrom !== undefined && dataUpdatedAt > reloadingFrom && !isFetching) {
    setReloadingFrom(undefined);
    setGeneration((n) => n + 1);
  }
  if (
    reloadingFrom !== undefined &&
    query.isError &&
    !isFetching &&
    query.errorUpdatedAt > reloadingFrom
  ) {
    setReloadingFrom(undefined);
  }
  const reload = () => {
    setReloadingFrom(dataUpdatedAt);
    void refetch();
  };
  return (
    <div className="flex flex-col gap-6">
      <div className="flex flex-col gap-2">
        <Link
          to="/escalation-chains"
          search={chain?.owning_team == null ? {} : { team: chain.owning_team.id }}
          className={buttonVariants({ variant: "link", className: "w-fit px-0" })}
        >
          <ArrowLeftIcon aria-hidden="true" />
          {t("escalation.title")}
        </Link>
        <div className="flex min-w-0 flex-col items-start gap-1">
          <PageTitle docs="page.escalation_chain">
            {chain?.name ?? t("escalation.page.title")}
          </PageTitle>
          <ManagedByBadge tool={chain?.managed_by} />
        </div>
        {chain?.owning_team != null && (
          <p className="text-sm text-muted-foreground">
            {t("escalation.page.ownedBy")}{" "}
            <Link
              to="/teams/$teamId"
              params={{ teamId: chain.owning_team.id }}
              className="text-primary underline-offset-4 hover:underline focus-visible:underline"
            >
              {chain.owning_team.name}
            </Link>
          </p>
        )}
      </div>
      {query.isError && chain !== undefined && (
        <Alert variant="destructive" data-testid="chain-read-error">
          <AlertDescription className="text-current">
            {problemText(t, query.error)}
          </AlertDescription>
        </Alert>
      )}
      {chain === undefined ? (
        <p className="text-sm text-muted-foreground" role="status">
          {query.isError ? problemText(t, query.error) : t("common.loading")}
        </p>
      ) : (
        <ChainBody key={generation} chain={chain} reload={reload} />
      )}
    </div>
  );
}

function ChainPage() {
  const { chainId } = Route.useParams();
  return (
    <RequirePermission permission="escalation-chains:read">
      <ChainView chainId={chainId} />
    </RequirePermission>
  );
}
