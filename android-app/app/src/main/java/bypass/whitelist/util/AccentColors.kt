package bypass.whitelist.util

import android.content.Context
import bypass.whitelist.R
import androidx.core.graphics.ColorUtils
import com.google.android.material.color.MaterialColors

object AccentColors {
    fun primary(context: Context): Int = MaterialColors.getColor(
        context,
        androidx.appcompat.R.attr.colorPrimary,
        context.getColor(R.color.accent_emerald),
    )

    fun container(context: Context): Int = ColorUtils.setAlphaComponent(primary(context), 0x30)
}
