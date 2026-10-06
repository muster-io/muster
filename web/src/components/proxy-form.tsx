// SPDX-License-Identifier: AGPL-3.0-only
// Copyright The Muster Authors

// The shared proxy form (C-03.FR-19, C-02.FR-22): use a proxy or not, its type, address, username and a write-only
// password. The OIDC settings are its first user; Connections, outgoing webhooks and the outgoing heartbeat reuse it.
// It is a controlled part of the page's form: the page keeps ProxyValues and sends proxyInput(values).

import type { ReactNode } from "react";
import { useTranslation } from "react-i18next";

import type { ProxyConfig, ProxyConfigInput, ProxyType, SecretStatus } from "../api/gen/model";
import { KEEP_SECRET, type SecretChange, SecretField, secretPayload } from "./secret-field";
import { Input } from "./ui/input";
import { Label } from "./ui/label";
import { NativeSelect, NativeSelectOption } from "./ui/native-select";

export interface ProxyValues {
  enabled: boolean;
  type: ProxyType;
  address: string;
  username: string;
  password: SecretChange;
}

export type ProxyField = "type" | "address" | "username" | "password";

/** The form values of a proxy as read; the password only as its status, which the form shows apart. */
export function proxyValues(config: ProxyConfig | undefined): ProxyValues {
  return {
    enabled: config?.enabled ?? false,
    type: config?.type ?? "http",
    address: config?.address ?? "",
    username: config?.username ?? "",
    password: KEEP_SECRET,
  };
}

/** The proxy of an update: an empty username is cleared, and the password follows the rule of every Secret. */
export function proxyInput(values: ProxyValues): ProxyConfigInput {
  const input: ProxyConfigInput = {
    enabled: values.enabled,
    type: values.type,
    username: values.username.trim() === "" ? null : values.username.trim(),
  };
  const address = values.address.trim();
  if (address !== "") {
    input.address = address;
  }
  const password = secretPayload(values.password);
  if (password !== undefined) {
    input.password = password;
  }
  return input;
}

/** The checks the server repeats: an enabled proxy needs an address. Returns the field errors as codes. */
export function proxyErrors(values: ProxyValues): Partial<Record<ProxyField, string>> {
  return values.enabled && values.address.trim() === "" ? { address: "required" } : {};
}

/** A native checkbox with its label and an optional hint. */
export function CheckField({
  id,
  label,
  hint,
  checked,
  onChange,
  disabled = false,
}: {
  id: string;
  label: ReactNode;
  hint?: ReactNode;
  checked: boolean;
  onChange: (checked: boolean) => void;
  disabled?: boolean;
}) {
  return (
    <div className="flex items-start gap-2">
      <input
        id={id}
        type="checkbox"
        className="mt-0.5 size-4 shrink-0 accent-primary focus-visible:outline-2 focus-visible:outline-offset-2 focus-visible:outline-ring"
        checked={checked}
        disabled={disabled}
        aria-describedby={hint === undefined ? undefined : `${id}-hint`}
        onChange={(e) => onChange(e.target.checked)}
      />
      <div className="flex flex-col gap-1">
        <Label htmlFor={id} className="leading-snug">
          {label}
        </Label>
        {hint !== undefined && (
          <p id={`${id}-hint`} className="text-sm text-muted-foreground">
            {hint}
          </p>
        )}
      </div>
    </div>
  );
}

export interface ProxyFormProps {
  /** The base of the ids of the fields, unique on the page. */
  idPrefix: string;
  value: ProxyValues;
  onChange: (value: ProxyValues) => void;
  /** The status of the stored password. */
  passwordStatus: SecretStatus | undefined;
  /** Field errors, already translated. */
  errors?: Partial<Record<ProxyField, string>>;
  disabled?: boolean;
}

export function ProxyForm({
  idPrefix,
  value,
  onChange,
  passwordStatus,
  errors = {},
  disabled = false,
}: ProxyFormProps) {
  const { t } = useTranslation();
  const set = (patch: Partial<ProxyValues>) => onChange({ ...value, ...patch });
  const id = (name: string) => `${idPrefix}-${name}`;
  const described = (name: ProxyField) =>
    errors[name] === undefined ? undefined : `${id(name)}-error`;
  const fieldError = (name: ProxyField) =>
    errors[name] === undefined ? null : (
      <p id={`${id(name)}-error`} className="text-sm text-destructive">
        {errors[name]}
      </p>
    );
  return (
    <fieldset className="flex min-w-0 flex-col gap-4" disabled={disabled}>
      <legend className="mb-3 text-base font-semibold">{t("proxy.title")}</legend>
      <CheckField
        id={id("enabled")}
        label={t("proxy.enabled")}
        hint={t("proxy.enabledHint")}
        checked={value.enabled}
        disabled={disabled}
        onChange={(enabled) => set({ enabled })}
      />
      {value.enabled && (
        <div className="grid gap-4 sm:grid-cols-2">
          <div className="flex flex-col gap-2">
            <Label htmlFor={id("type")}>{t("proxy.type")}</Label>
            <NativeSelect
              id={id("type")}
              value={value.type}
              aria-invalid={errors.type !== undefined}
              aria-describedby={described("type")}
              onChange={(e) => {
                const type = (["http", "https", "socks5"] as const).find(
                  (v) => v === e.target.value,
                );
                if (type !== undefined) {
                  set({ type });
                }
              }}
            >
              <NativeSelectOption value="http">HTTP</NativeSelectOption>
              <NativeSelectOption value="https">HTTPS</NativeSelectOption>
              <NativeSelectOption value="socks5">SOCKS5</NativeSelectOption>
            </NativeSelect>
            {fieldError("type")}
          </div>
          <div className="flex flex-col gap-2">
            <Label htmlFor={id("address")}>{t("proxy.address")}</Label>
            <Input
              id={id("address")}
              value={value.address}
              placeholder="proxy.example.org:3128"
              autoComplete="off"
              spellCheck={false}
              aria-invalid={errors.address !== undefined}
              aria-describedby={described("address") ?? `${id("address")}-hint`}
              onChange={(e) => set({ address: e.target.value })}
            />
            {errors.address === undefined ? (
              <p id={`${id("address")}-hint`} className="text-sm text-muted-foreground">
                {t("proxy.addressHint")}
              </p>
            ) : (
              fieldError("address")
            )}
          </div>
          <div className="flex flex-col gap-2">
            <Label htmlFor={id("username")}>{t("proxy.username")}</Label>
            <Input
              id={id("username")}
              value={value.username}
              autoComplete="off"
              spellCheck={false}
              aria-invalid={errors.username !== undefined}
              aria-describedby={described("username")}
              onChange={(e) => set({ username: e.target.value })}
            />
            {fieldError("username")}
          </div>
          <SecretField
            id={id("password")}
            label={t("proxy.password")}
            status={passwordStatus}
            value={value.password}
            clearable
            disabled={disabled}
            error={errors.password}
            onChange={(password) => set({ password })}
          />
        </div>
      )}
    </fieldset>
  );
}
