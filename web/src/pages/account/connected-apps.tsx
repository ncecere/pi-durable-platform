/*
 * Account › Connected apps (/settings/connected-apps), from the account menu
 * next to Profile and API keys: the apps the person allowed to act as them
 * with OAuth Connect, with Disconnect. Reachable for everyone, including
 * people in no team.
 */
import { Link } from "@tanstack/react-router";
import { ConnectedApps } from "@/components/oauth/connected-apps";
import { Alert } from "@/components/ui/alert/alert";
import { TextLink } from "@/components/ui/text-link/text-link";
import { terms } from "@/lib/terms";

export function ConnectedAppsPage() {
  return (
    <ConnectedApps
      owner={{ self: true }}
      canDisconnect
      title={terms.connectedApps}
      description="Apps you allowed to act as you, such as a chat app that talks to your agents. Each one has your access to agents, never your admin powers. Disconnecting one stops it at once."
      notices={
        <Alert tone="info">
          An app asks to connect when you sign in to it with your platform account. Scripts and tools you run yourself use{" "}
          <TextLink render={<Link to="/settings/api-keys" />}>{terms.apiKeys}</TextLink> instead.
        </Alert>
      }
    />
  );
}
