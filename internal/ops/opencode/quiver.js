import { execFileSync } from "child_process";

// Quiver SkillOps plugin for OpenCode. Bridges OpenCode's tool and session
// hooks into `qvr _hook opencode <type>` by piping the event JSON on stdin.
// The __QUIVER_COMMAND__ placeholder is replaced with the resolved qvr
// binary path at install time. Observe-only: never throws on the agent's
// behalf (a non-zero exit is swallowed) so auditing can't block the user.
export const QuiverPlugin = async ({ directory }) => {
  function invokeQuiver(hookType, payload) {
    try {
      execFileSync("__QUIVER_COMMAND__", ["_hook", "opencode", hookType], {
        input: JSON.stringify(payload),
        stdio: ["pipe", "pipe", "pipe"],
        timeout: 5000,
      });
    } catch (_) {
      // Audit is best-effort; swallow errors so OpenCode is never blocked.
    }
  }

  return {
    "tool.execute.before": async (input, output) => {
      invokeQuiver("tool.execute.before", {
        hook_type: "tool.execute.before",
        session_id: input.sessionID,
        tool: input.tool,
        args: output.args,
        cwd: directory,
      });
    },
    "tool.execute.after": async (input, output) => {
      invokeQuiver("tool.execute.after", {
        hook_type: "tool.execute.after",
        session_id: input.sessionID,
        tool: input.tool,
        result: {
          title: output.title,
          output: output.output,
          metadata: output.metadata,
        },
        cwd: directory,
      });
    },
    event: async ({ event }) => {
      const type = event.type;
      if (!["session.created", "session.idle", "session.error"].includes(type))
        return;
      let sessionId = "";
      if (type === "session.created") {
        sessionId = event.properties?.info?.id || "";
      } else {
        sessionId = event.properties?.sessionID || "";
      }
      invokeQuiver(type, {
        hook_type: type,
        properties: { sessionId, ...event.properties },
        cwd: directory,
      });
    },
  };
};
