package dev.monitor.server

import android.content.Context
import android.content.SharedPreferences

/** User-editable server settings, rendered into monitor.yaml on every start. */
class Settings(context: Context) {
    private val p: SharedPreferences = context.getSharedPreferences("monitord", Context.MODE_PRIVATE)

    var port: Int
        get() = p.getInt("port", 19999)
        set(v) = p.edit().putInt("port", v).apply()

    var hubUrl: String
        get() = p.getString("hub_url", "") ?: ""
        set(v) = p.edit().putString("hub_url", v).apply()

    var apiKey: String
        get() = p.getString("api_key", "") ?: ""
        set(v) = p.edit().putString("api_key", v).apply()

    var startOnBoot: Boolean
        get() = p.getBoolean("boot", false)
        set(v) = p.edit().putBoolean("boot", v).apply()

    /** YAML for monitord. Collectors that need /proc/net or root are disabled. */
    fun toYaml(dataDir: String): String {
        val stream = if (hubUrl.isNotBlank() && apiKey.isNotBlank()) {
            """
            |stream:
            |  enabled: true
            |  destinations: ["${hubUrl.trim()}"]
            |  api_key: "${apiKey.trim()}"
            """.trimMargin()
        } else {
            "stream:\n  enabled: false"
        }
        return """
        |global:
        |  data_dir: "$dataDir"
        |web:
        |  listen: ":$port"
        |collectors:
        |  disabled: [docker, systemd, nginx, redis]
        |$stream
        |""".trimMargin()
    }
}
