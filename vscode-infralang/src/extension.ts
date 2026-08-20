import { access } from "node:fs/promises";

import * as vscode from "vscode";
import {
  LanguageClient,
  LanguageClientOptions,
  RevealOutputChannelOn,
  ServerOptions,
} from "vscode-languageclient/node";

import { resolveServerCommand } from "./serverPath";

const configurationSection = "infralang.languageServer";
const restartCommand = "infralang.restartLanguageServer";

let client: LanguageClient | undefined;
let outputChannel: vscode.OutputChannel | undefined;
let fileWatcher: vscode.FileSystemWatcher | undefined;

export async function activate(
  context: vscode.ExtensionContext,
): Promise<void> {
  outputChannel = vscode.window.createOutputChannel(
    "InfraLang Language Server",
  );
  context.subscriptions.push(outputChannel);

  context.subscriptions.push(
    vscode.commands.registerCommand(restartCommand, async () => {
      await stopLanguageClient();
      await startLanguageClient(context);
    }),
  );

  await startLanguageClient(context);
}

export async function deactivate(): Promise<void> {
  await stopLanguageClient();
  outputChannel = undefined;
}

async function startLanguageClient(
  context: vscode.ExtensionContext,
): Promise<void> {
  const channel = outputChannel;
  if (channel === undefined) {
    return;
  }
  const configuration = vscode.workspace.getConfiguration(configurationSection);
  if (!configuration.get<boolean>("enable", true)) {
    channel.appendLine("Language server is disabled by configuration.");
    return;
  }
  if (client !== undefined) {
    return;
  }

  const configuredPath = configuration.get<string>("path", "");
  const args = configuration.get<string[]>("args", []);
  const command = resolveServerCommand(context.extensionPath, configuredPath);

  if (configuredPath.trim() === "") {
    try {
      await access(command);
    } catch {
      const message = `Bundled InfraLang language server was not found at ${command}. Build it with npm run build-server or configure infralang.languageServer.path.`;
      channel.appendLine(message);
      void vscode.window.showErrorMessage(message);
      return;
    }
  }

  fileWatcher?.dispose();
  fileWatcher = vscode.workspace.createFileSystemWatcher("**/*.infra");

  const serverOptions: ServerOptions = {
    command,
    args,
    options: {
      cwd:
        vscode.workspace.workspaceFolders?.[0]?.uri.fsPath ??
        context.extensionPath,
    },
  };
  const clientOptions: LanguageClientOptions = {
    documentSelector: [{ language: "infralang", scheme: "file" }],
    diagnosticCollectionName: "infralang",
    outputChannel: channel,
    revealOutputChannelOn: RevealOutputChannelOn.Never,
    synchronize: {
      fileEvents: fileWatcher,
    },
    initializationOptions: {
      providerSchemas:
        vscode.workspace.isTrusted &&
        configuration.get<boolean>("providerSchema.enable", true),
    },
  };

  const nextClient = new LanguageClient(
    "infralang",
    "InfraLang Language Server",
    serverOptions,
    clientOptions,
  );
  client = nextClient;
  channel.appendLine(
    `Starting ${command} with ${args.length} configured argument${args.length === 1 ? "" : "s"}.`,
  );

  try {
    await nextClient.start();
  } catch (error: unknown) {
    if (client === nextClient) {
      client = undefined;
    }
    fileWatcher?.dispose();
    fileWatcher = undefined;
    const detail = error instanceof Error ? error.message : String(error);
    const message = `Failed to start the InfraLang language server: ${detail}`;
    channel.appendLine(message);
    void vscode.window
      .showErrorMessage(message, "Show Output")
      .then((selection) => {
        if (selection === "Show Output") {
          channel.show(true);
        }
      });
  }
}

async function stopLanguageClient(): Promise<void> {
  const currentClient = client;
  client = undefined;
  try {
    if (currentClient !== undefined) {
      await currentClient.stop();
    }
  } finally {
    fileWatcher?.dispose();
    fileWatcher = undefined;
  }
}
