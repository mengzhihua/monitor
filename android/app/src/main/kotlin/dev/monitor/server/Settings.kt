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

    /** YAML for monitord. Linux-only collectors and those needing /proc/net or root are disabled; logs use logcat. */
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
        |  disabled: [docker, systemd, nginx, redis, cgroup, k8s_kubelet, k8s_kubeproxy, k8s_apiserver, k8s_state, nvidia, intelgpu, dcgm, ethtool, ap, logind, smbios_memory, wireguard, zfspool, lvm, nvme, smartctl, megacli, hpssa, adaptecraid, storcli, libvirt, proxmox, ebpf, cups, xenstat, ioping, nftables, podman, ipmi, perf, nfacct]
        |  modules:
        |    logs: {}
        |$stream
        |""".trimMargin()
    }
}
