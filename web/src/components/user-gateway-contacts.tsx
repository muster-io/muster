// SPDX-License-Identifier: AGPL-3.0-only
// Copyright The Muster Authors

// A User's contacts at Notification gateways on the User's page (C-30.FR-3, C-18.FR-6): the gateway, the contact and
// its state, confirmed or not, with "Remove" for users:write, which asks first. There is no way to add one: only the
// person sets their contact, from their own web session (ADR-0013). The Audit log shows the entries gateway_contact.*.

import { useMutation, useQueryClient } from "@tanstack/react-query";
import { useState } from "react";
import { useTranslation } from "react-i18next";

import {
  deleteUserGatewayContact,
  getListUserGatewayContactsQueryKey,
  useListUserGatewayContacts,
} from "../api/gen/endpoints/notification-gateways/notification-gateways";
import type { GatewayContact, User } from "../api/gen/model";
import { problemText } from "../lib/api";
import { RemoveContactDialog } from "./gateway-contact-section";
import { Button } from "./ui/button";
import { Card, CardContent, CardDescription, CardHeader, CardTitle } from "./ui/card";

export function UserGatewayContacts({ user, canWrite }: { user: User; canWrite: boolean }) {
  const { t } = useTranslation();
  const queryClient = useQueryClient();
  const list = useListUserGatewayContacts(user.id);
  const [asking, setAsking] = useState<GatewayContact | null>(null);
  const remove = useMutation({
    mutationFn: (contact: GatewayContact) => deleteUserGatewayContact(user.id, contact.id),
    onSuccess: () => {
      setAsking(null);
      void queryClient.invalidateQueries({ queryKey: getListUserGatewayContactsQueryKey(user.id) });
    },
  });
  const items = list.data?.items ?? [];
  return (
    <Card className="lg:col-span-2" data-testid="user-gateway-contacts">
      <CardHeader>
        <CardTitle>
          <h2>{t("userContacts.title")}</h2>
        </CardTitle>
        <CardDescription>{t("userContacts.hint")}</CardDescription>
      </CardHeader>
      <CardContent className="flex flex-col gap-3">
        {items.length === 0 ? (
          <p className="text-sm text-muted-foreground" role="status">
            {list.isPending
              ? t("common.loading")
              : list.isError
                ? problemText(t, list.error)
                : t("userContacts.empty")}
          </p>
        ) : (
          <div className="overflow-x-auto rounded-lg border">
            <table className="w-full text-left text-sm" aria-label={t("userContacts.title")}>
              <thead className="border-b bg-muted/40 text-muted-foreground">
                <tr>
                  <th scope="col" className="px-3 py-2 font-medium">
                    {t("userContacts.gateway")}
                  </th>
                  <th scope="col" className="px-3 py-2 font-medium">
                    {t("userContacts.contact")}
                  </th>
                  <th scope="col" className="px-3 py-2 font-medium">
                    {t("userContacts.state")}
                  </th>
                  {canWrite && (
                    <th scope="col" className="px-3 py-2 font-medium">
                      <span className="sr-only">{t("userContacts.actions")}</span>
                    </th>
                  )}
                </tr>
              </thead>
              <tbody className="divide-y">
                {items.map((c) => (
                  <tr
                    key={c.id}
                    data-testid="user-gateway-contact"
                    data-state={c.confirmed ? "confirmed" : "unconfirmed"}
                  >
                    <td className="px-3 py-2 wrap-anywhere">{c.connection.name}</td>
                    <td className="px-3 py-2 wrap-anywhere">{c.contact}</td>
                    <td className="px-3 py-2">
                      {c.confirmed ? t("gatewayContact.confirmed") : t("userContacts.unconfirmed")}
                    </td>
                    {canWrite && (
                      <td className="px-3 py-2">
                        <Button
                          variant="outline"
                          size="sm"
                          aria-label={t("userContacts.removeLabel", { contact: c.contact })}
                          onClick={() => {
                            remove.reset();
                            setAsking(c);
                          }}
                        >
                          {t("gatewayContact.remove.action")}
                        </Button>
                      </td>
                    )}
                  </tr>
                ))}
              </tbody>
            </table>
          </div>
        )}
      </CardContent>
      {canWrite && (
        <RemoveContactDialog
          contact={asking}
          description={(c) =>
            t("userContacts.removeConfirm", {
              contact: c.contact,
              gateway: c.connection.name,
              user: user.name,
            })
          }
          pending={remove.isPending}
          error={remove.error}
          onOpenChange={(open) => {
            if (!open) {
              setAsking(null);
            }
          }}
          onConfirm={(c) => remove.mutate(c)}
        />
      )}
    </Card>
  );
}
