package dev.monitor.server

import android.app.usage.UsageStatsManager
import android.content.Context
import org.json.JSONArray
import org.json.JSONObject
import java.io.File

/** Writes UsageStats snapshots for the android_usage collector. */
object UsageSampler {
    fun write(ctx: Context) {
        val apps = JSONArray()
        val usm = ctx.getSystemService(Context.USAGE_STATS_SERVICE) as? UsageStatsManager
        val end = System.currentTimeMillis()
        val begin = end - 60_000
        usm?.queryUsageStats(UsageStatsManager.INTERVAL_DAILY, begin, end)?.forEach { st ->
            if (st.totalTimeInForeground <= 0) return@forEach
            apps.put(
                JSONObject()
                    .put("name", st.packageName)
                    .put("cpu_ms", st.totalTimeInForeground)
                    .put("rx", 0)
                    .put("tx", 0),
            )
        }
        File(ctx.filesDir, "android-usage.json").writeText(JSONObject().put("apps", apps).toString())
    }
}
