// SPDX-License-Identifier: AGPL-3.0-only
// Copyright The Muster Authors

import { Link } from "@tanstack/react-router";
import { ChevronDownIcon, LogOutIcon, UserIcon } from "lucide-react";
import { useTranslation } from "react-i18next";

import { deleteCurrentSession } from "../api/gen/endpoints/sessions/sessions";
import type { User } from "../api/gen/model";
import { Button } from "./ui/button";
import {
  DropdownMenu,
  DropdownMenuContent,
  DropdownMenuGroup,
  DropdownMenuItem,
  DropdownMenuLabel,
  DropdownMenuSeparator,
  DropdownMenuTrigger,
} from "./ui/dropdown-menu";

/** Ends the session and opens the sign-in page; a full load drops every cached answer of the user. */
export async function signOut(): Promise<void> {
  try {
    await deleteCurrentSession();
  } finally {
    window.location.assign("/sign-in");
  }
}

export function UserMenu({ user }: { user: Pick<User, "name" | "login"> }) {
  const { t } = useTranslation();
  return (
    <DropdownMenu>
      <DropdownMenuTrigger
        render={
          <Button
            variant="ghost"
            className="max-w-32 sm:max-w-56"
            aria-label={t("userMenu.label", { name: user.name })}
          />
        }
      >
        <UserIcon aria-hidden="true" />
        <span className="truncate" data-testid="user-menu-name">
          {user.name}
        </span>
        <ChevronDownIcon aria-hidden="true" />
      </DropdownMenuTrigger>
      <DropdownMenuContent align="end" className="w-auto min-w-48">
        <DropdownMenuGroup>
          <DropdownMenuLabel className="truncate">{user.login}</DropdownMenuLabel>
        </DropdownMenuGroup>
        <DropdownMenuSeparator />
        <DropdownMenuItem render={<Link to="/profile" />}>
          <UserIcon aria-hidden="true" />
          {t("nav.profile")}
        </DropdownMenuItem>
        <DropdownMenuItem onClick={() => void signOut()}>
          <LogOutIcon aria-hidden="true" />
          {t("userMenu.signOut")}
        </DropdownMenuItem>
      </DropdownMenuContent>
    </DropdownMenu>
  );
}
