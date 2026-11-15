"use strict";

const { spawnSync } = require("child_process");

const SERVICE = "claude-custom";
const cache = new Map();

const SCRIPT = `$ErrorActionPreference = 'Stop'
Add-Type -TypeDefinition @"
using System;
using System.Runtime.InteropServices;
public static class ClaudeCustomCred {
  [StructLayout(LayoutKind.Sequential, CharSet = CharSet.Unicode)]
  public struct CREDENTIAL {
    public int Flags;
    public int Type;
    public IntPtr TargetName;
    public IntPtr Comment;
    public long LastWritten;
    public int CredentialBlobSize;
    public IntPtr CredentialBlob;
    public int Persist;
    public int AttributeCount;
    public IntPtr Attributes;
    public IntPtr TargetAlias;
    public IntPtr UserName;
  }
  [DllImport("advapi32.dll", EntryPoint = "CredReadW", CharSet = CharSet.Unicode, SetLastError = true)]
  public static extern bool CredRead(string target, int type, int reservedFlag, out IntPtr credentialPtr);
  [DllImport("advapi32.dll", SetLastError = true)]
  public static extern void CredFree(IntPtr cred);
  public static string Read(string target) {
    IntPtr ptr;
    if (!CredRead(target, 1, 0, out ptr)) return null;
    try {
      CREDENTIAL cred = (CREDENTIAL)Marshal.PtrToStructure(ptr, typeof(CREDENTIAL));
      if (cred.CredentialBlobSize <= 0) return null;
      byte[] bytes = new byte[cred.CredentialBlobSize];
      Marshal.Copy(cred.CredentialBlob, bytes, 0, cred.CredentialBlobSize);
      return System.Text.Encoding.UTF8.GetString(bytes);
    } finally {
      CredFree(ptr);
    }
  }
}
"@
$value = [ClaudeCustomCred]::Read('TARGET')
if ($null -ne $value) { [Console]::Out.Write($value) }
`;

function safeName(provider) {
  return /^[A-Za-z0-9_.-]+$/.test(String(provider || ""));
}

function readCredential(provider) {
  if (process.platform !== "win32") return "";
  if (!safeName(provider)) return "";
  if (cache.has(provider)) return cache.get(provider);
  const target = `${SERVICE}:${provider}`.replace(/'/g, "''");
  const script = SCRIPT.replace("TARGET", target);
  let secret = "";
  try {
    const result = spawnSync(
      "powershell.exe",
      ["-NoProfile", "-NonInteractive", "-ExecutionPolicy", "Bypass", "-Command", script],
      { encoding: "utf8", timeout: 5000, windowsHide: true, maxBuffer: 1 << 20 }
    );
    if (result.status === 0 && result.stdout) secret = String(result.stdout).trim();
  } catch (err) {
    secret = "";
  }
  cache.set(provider, secret);
  return secret;
}

module.exports = { readCredential };
