// SPDX-License-Identifier: MIT
// Copyright (c) 2023 shadcn
// Copied from shadcn/ui (https://ui.shadcn.com), lib/utils.ts.

import { clsx, type ClassValue } from "clsx";
import { twMerge } from "tailwind-merge";

export function cn(...inputs: ClassValue[]) {
  return twMerge(clsx(inputs));
}
