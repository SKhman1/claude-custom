"use strict";

const commands = require("./commands.js");
const prompt = require("./prompt.js");
const claude = require("./claude.js");
const { UsageError } = require("./flags.js");

const pkg = require("../package.json");

const USAGE = `claude-custom - run Claude Code against any OpenAI-compatible model

Usage:
  claude-custom                    Choose a profile and launch Claude Code
  claude-custom run [flags] [-- claude args...]
  claude-custom status             Show proxy and profile status
  claude-custom doctor             Check the local setup

  claude-custom serve [flags]      Run the local proxy in the foreground
  claude-custom stop               Stop a running proxy
  claude-custom logs [-f] [--lines N]  Read the proxy log

  claude-custom setup [flags]      Reconfigure provider/model or script setup
  claude-custom profile list|use <name>|show [name]|remove <name>
  claude-custom provider list
  claude-custom model list [--profile <name>] [--refresh]
  claude-custom key set <profile>|remove <profile>|status <profile>
  claude-custom config path|show
  claude-custom version            Print the version

Run "claude-custom <command> -h" for command flags.
`;

async function run(args) {
  if (!args.length) {
    await commands.defaultCommand();
    return 0;
  }
  const [cmd, ...rest] = args;
  switch (cmd) {
    case "help":
    case "-h":
    case "--help":
      process.stdout.write(USAGE);
      return 0;
    case "version":
    case "--version":
      process.stdout.write(`claude-custom ${pkg.version}\n`);
      return 0;
    case "setup":
      await commands.setupCommand(rest);
      return 0;
    case "run":
      return commands.runCommand(rest);
    case "serve":
      await commands.serveCommand(rest);
      return 0;
    case "stop":
      await commands.stopCommand();
      return 0;
    case "status":
      await commands.statusCommand();
      return 0;
    case "doctor":
      await commands.doctorCommand();
      return 0;
    case "logs":
      await commands.logsCommand(rest);
      return 0;
    case "profile":
      await commands.profileCommand(rest);
      return 0;
    case "provider":
      await commands.providerCommand(rest);
      return 0;
    case "model":
      await commands.modelCommand(rest);
      return 0;
    case "key":
      await commands.keyCommand(rest);
      return 0;
    case "config":
      await commands.configCommand(rest);
      return 0;
    default:
      throw new UsageError(`unknown command "${cmd}"; run \`claude-custom help\``);
  }
}

async function main(argv) {
  try {
    const code = await run(argv);
    prompt.close();
    process.exit(code);
  } catch (err) {
    prompt.close();
    if (err instanceof prompt.CancelledError) {
      process.exit(0);
    }
    if (err instanceof claude.ExitError) {
      process.exit(err.exitCode);
    }
    if (err instanceof UsageError) {
      process.stderr.write(`claude-custom: ${err.message}\n`);
      if (err.message.startsWith("unknown command")) process.stderr.write(USAGE);
      process.exit(1);
    }
    process.stderr.write(`claude-custom: ${err && err.message ? err.message : err}\n`);
    process.exit(1);
  }
}

module.exports = { main, run, USAGE };

if (require.main === module) {
  main(process.argv.slice(2));
}
