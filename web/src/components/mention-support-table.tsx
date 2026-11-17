// SPDX-License-Identifier: AGPL-3.0-only
// Copyright The Muster Authors

// "What each messenger supports" (C-28.FR-2): the table of what each type of Destination can mention — everyone in the
// chat, messenger groups and Teams, people and the On-call — shown with the Mentions grid of every type.

import { useId } from "react";
import { useTranslation } from "react-i18next";

import { DocsLink } from "./docs-link";

export function MentionSupportTable() {
  const { t } = useTranslation();
  const title = useId();
  const rows = [
    {
      id: "mattermost",
      name: t("mentions.messengers.mattermost"),
      everyone: t("mentions.support.mattermost.everyone"),
      groups: t("mentions.support.mattermost.groups"),
      people: t("mentions.support.linkedPeople"),
    },
    {
      id: "slack",
      name: t("mentions.messengers.slack"),
      everyone: t("mentions.support.slack.everyone"),
      groups: t("mentions.support.slack.groups"),
      people: t("mentions.support.linkedPeople"),
    },
    {
      id: "telegram",
      name: t("mentions.messengers.telegram"),
      everyone: t("mentions.support.none"),
      groups: t("mentions.support.none"),
      people: t("mentions.support.linkedPeople"),
    },
    {
      id: "bridge",
      name: t("mentions.messengers.bridge"),
      everyone: t("mentions.support.bridge.everyone"),
      groups: t("mentions.support.bridge.groups"),
      people: t("mentions.support.bridge.people"),
    },
    {
      id: "webhook",
      name: t("mentions.messengers.webhook"),
      everyone: t("mentions.support.webhook"),
      groups: t("mentions.support.webhook"),
      people: t("mentions.support.webhook"),
    },
  ];
  return (
    <section
      aria-labelledby={title}
      className="flex min-w-0 flex-col gap-2"
      data-testid="mention-support"
    >
      <h3 className="flex flex-wrap items-baseline gap-x-2 text-sm font-semibold">
        <span id={title}>{t("mentions.support.title")}</span>
        <DocsLink
          to="field.mention_support"
          kind="learn_more"
          about={t("docs.fields.mentionSupport")}
        />
      </h3>
      <div className="min-w-0 overflow-x-auto rounded-lg border">
        <table className="w-full min-w-[40rem] border-collapse text-left text-sm">
          <thead className="bg-muted/50 text-xs text-muted-foreground">
            <tr>
              <th scope="col" className="px-3 py-2 font-medium">
                {t("mentions.support.messenger")}
              </th>
              <th scope="col" className="px-3 py-2 font-medium">
                {t("mentions.support.everyone")}
              </th>
              <th scope="col" className="px-3 py-2 font-medium">
                {t("mentions.support.groups")}
              </th>
              <th scope="col" className="px-3 py-2 font-medium">
                {t("mentions.support.people")}
              </th>
            </tr>
          </thead>
          <tbody>
            {rows.map((row) => (
              <tr
                key={row.id}
                className="border-t align-top"
                data-testid={`mention-support-${row.id}`}
              >
                <th scope="row" className="px-3 py-2 font-medium">
                  {row.name}
                </th>
                <td className="px-3 py-2">{row.everyone}</td>
                <td className="px-3 py-2">{row.groups}</td>
                <td className="px-3 py-2">{row.people}</td>
              </tr>
            ))}
          </tbody>
        </table>
      </div>
    </section>
  );
}
