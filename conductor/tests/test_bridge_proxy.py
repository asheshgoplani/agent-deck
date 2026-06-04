"""Tests for proxy support in bridge.py's create_telegram_bot.

Covers the feature that allows the Telegram bot to connect through an HTTP proxy
when environment variables (HTTPS_PROXY, HTTP_PROXY, etc.) are set.

Proxy env var precedence (highest wins):
  HTTPS_PROXY > https_proxy > HTTP_PROXY > http_proxy

Design: We test the *decision logic* (which env var is picked, whether
AiohttpSession is called at all) without requiring aiohttp_socks to be
installed. The actual AiohttpSession constructor is mocked so the test
runs on any Python that can import bridge.py.
"""

from __future__ import annotations

import os
import sys
from pathlib import Path
from unittest import mock

import pytest

sys.path.insert(0, str(Path(__file__).parent.parent))

from bridge import create_telegram_bot, HAS_AIOGRAM  # noqa: E402


def _base_config() -> dict:
    """Minimal config dict with Telegram configured."""
    return {
        "telegram": {
            "token": "123456:TEST-TOKEN",
            "user_id": 999999,
            "configured": True,
        },
        "slack": {
            "bot_token": "",
            "app_token": "",
            "channel_id": "",
            "listen_mode": "mentions",
            "allowed_user_ids": [],
            "configured": False,
        },
        "heartbeat_interval": 15,
    }


# Env-var keys we patch for each test case.
PROXY_KEYS = ["HTTPS_PROXY", "https_proxy", "HTTP_PROXY", "http_proxy"]


@pytest.fixture(autouse=True)
def clean_proxy_env():
    """Ensure no proxy env vars leak between tests."""
    saved = {}
    for key in PROXY_KEYS:
        if key in os.environ:
            saved[key] = os.environ.pop(key)
    yield
    for key, val in saved.items():
        os.environ[key] = val


pytestmark = pytest.mark.skipif(
    not HAS_AIOGRAM, reason="aiogram not installed — skipping Telegram tests"
)


class TestProxyEnvVarPrecedence:
    """Verify that the correct proxy env var is picked when multiple are set."""

    def test_https_proxy_wins_over_http_proxy(self):
        with mock.patch.dict(os.environ, {
            "HTTPS_PROXY": "http://proxy-h:8080",
            "HTTP_PROXY": "http://proxy-l:3128",
        }, clear=False):
            with mock.patch("bridge.AiohttpSession") as mock_session_cls:
                mock_session = mock.MagicMock()
                mock_session_cls.return_value = mock_session
                with mock.patch("bridge.Bot") as mock_bot:
                    create_telegram_bot(_base_config())
                    mock_session_cls.assert_called_once_with(proxy="http://proxy-h:8080")

    def test_uppercase_https_proxy_wins_over_lowercase(self):
        """bridge.py checks HTTPS_PROXY before https_proxy."""
        with mock.patch.dict(os.environ, {
            "https_proxy": "http://proxy-lc:8080",
            "HTTPS_PROXY": "http://proxy-uc:3128",
        }, clear=False):
            with mock.patch("bridge.AiohttpSession") as mock_session_cls:
                mock_session = mock.MagicMock()
                mock_session_cls.return_value = mock_session
                with mock.patch("bridge.Bot") as mock_bot:
                    create_telegram_bot(_base_config())
                    mock_session_cls.assert_called_once_with(proxy="http://proxy-uc:3128")

    def test_http_proxy_fallback(self):
        with mock.patch.dict(os.environ, {
            "HTTP_PROXY": "http://proxy-fallback:8080",
        }, clear=False):
            with mock.patch("bridge.AiohttpSession") as mock_session_cls:
                mock_session = mock.MagicMock()
                mock_session_cls.return_value = mock_session
                with mock.patch("bridge.Bot") as mock_bot:
                    create_telegram_bot(_base_config())
                    mock_session_cls.assert_called_once_with(proxy="http://proxy-fallback:8080")

    def test_lowercase_http_proxy_fallback(self):
        with mock.patch.dict(os.environ, {
            "http_proxy": "http://proxy-lower:8080",
        }, clear=False):
            with mock.patch("bridge.AiohttpSession") as mock_session_cls:
                mock_session = mock.MagicMock()
                mock_session_cls.return_value = mock_session
                with mock.patch("bridge.Bot") as mock_bot:
                    create_telegram_bot(_base_config())
                    mock_session_cls.assert_called_once_with(proxy="http://proxy-lower:8080")


class TestNoProxyEnv:
    """When no proxy env vars are set, the bot should use the default (no proxy)."""

    def test_no_proxy_env_var(self):
        with mock.patch("bridge.AiohttpSession") as mock_session_cls:
            with mock.patch("bridge.Bot") as mock_bot:
                create_telegram_bot(_base_config())
                mock_session_cls.assert_not_called()
                mock_bot.assert_called_once_with(token="123456:TEST-TOKEN")


class TestProxyNotConfigured:
    """When Telegram is not configured, create_telegram_bot returns None."""

    def test_telegram_not_configured(self):
        config = _base_config()
        config["telegram"]["configured"] = False
        result = create_telegram_bot(config)
        assert result is None


class TestProxyLogging:
    """Verify that proxy usage is logged."""

    def test_proxy_usage_logged(self, caplog):
        import logging
        with mock.patch.dict(os.environ, {
            "HTTP_PROXY": "http://log-test:8080",
        }, clear=False):
            with mock.patch("bridge.AiohttpSession"):
                with mock.patch("bridge.Bot"):
                    with caplog.at_level(logging.INFO):
                        create_telegram_bot(_base_config())
        assert "Using proxy for Telegram bot" in caplog.text
        assert "http://log-test:8080" in caplog.text
