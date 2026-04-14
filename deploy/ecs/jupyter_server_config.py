"""
Jupyter Server configuration for scuttlebot spoke.

Configures jupyter-server-proxy to route /scuttlebot → scuttlebot HTTP API on port 8080.
absolute_url=False strips the /scuttlebot prefix so the Go server sees clean paths.
"""
import os

from traitlets.config import get_config

c = get_config()

c.ServerApp.allow_origin = "*"
c.ServerApp.allow_credentials = True
c.ServerApp.disable_check_xsrf = True
c.ServerApp.trust_xheaders = True
c.ServerApp.default_url = "/scuttlebot"

c.ServerApp.jpserver_extensions = {
    "jupyter_server_proxy": True,
}

c.ServerProxy.servers = {
    "scuttlebot": {
        "command": ["echo", "Scuttlebot is already running on port 8080"],
        "port": 8080,
        "timeout": 120,
        "absolute_url": False,
        "launcher_entry": {"enabled": False},
        "new_browser_tab": False,
    }
}

c.ServerProxy.host_allowlist = ["localhost", "127.0.0.1", "0.0.0.0"]

# Fix OAuth callback URL for named servers
if os.environ.get("JUPYTERHUB_SERVER_NAME"):
    server_name = os.environ["JUPYTERHUB_SERVER_NAME"]
    username = os.environ.get("JUPYTERHUB_USER", "")
    correct_callback = f"/user/{username}/{server_name}/oauth_callback"
    if os.environ.get("JUPYTERHUB_OAUTH_CALLBACK_URL") != correct_callback:
        os.environ["JUPYTERHUB_OAUTH_CALLBACK_URL"] = correct_callback

print("jupyter_server_config.py loaded: scuttlebot → port 8080 (prefix-stripped)")
