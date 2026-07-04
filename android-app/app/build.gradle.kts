plugins {
    alias(libs.plugins.android.application)
    alias(libs.plugins.kotlin.android)
}

val releaseVersion = System.getenv("RELEASE_VERSION") ?: "0.3.7"
val versionParts = releaseVersion.split('.')
require(versionParts.size == 3 && versionParts.all { part -> part.toIntOrNull() != null }) {
    "RELEASE_VERSION must use MAJOR.MINOR.PATCH format"
}
val (versionMajor, versionMinor, versionPatch) = versionParts.map(String::toInt)
require(versionMajor <= 210 && versionMinor <= 99 && versionPatch <= 99) {
    "RELEASE_VERSION exceeds Android versionCode limits"
}
val versionBuild = (System.getenv("BUILD_NUMBER")?.toIntOrNull() ?: 0).coerceIn(0, 999)

android {
    namespace = "app.vconnect"
    compileSdk {
        version = release(36)
    }

    defaultConfig {
        applicationId = "app.vconnect"
        minSdk = 23
        targetSdk = 36
        versionCode = 10_000_000 * versionMajor + 100_000 * versionMinor + 1_000 * versionPatch + versionBuild
        versionName = releaseVersion

        testInstrumentationRunner = "androidx.test.runner.AndroidJUnitRunner"
    }

    signingConfigs {
        getByName("debug") {
            storeFile = file("../debug.keystore")
            storePassword = "android"
            keyAlias = "debug"
            keyPassword = "android"
        }
    }

    buildTypes {
        debug {
            signingConfig = signingConfigs.getByName("debug")
        }
        release {
            isMinifyEnabled = false
            signingConfig = signingConfigs.getByName("debug")
            proguardFiles(
                getDefaultProguardFile("proguard-android-optimize.txt"),
                "proguard-rules.pro"
            )
        }
    }
    compileOptions {
        sourceCompatibility = JavaVersion.VERSION_11
        targetCompatibility = JavaVersion.VERSION_11
    }
    kotlinOptions {
        jvmTarget = "11"
    }
}

dependencies {
    implementation(fileTree(mapOf("dir" to "libs", "include" to listOf("*.aar"))))
    implementation(libs.androidx.core.ktx)
    implementation(libs.androidx.appcompat)
    implementation(libs.material)
    implementation(libs.androidx.activity)
    implementation(libs.androidx.constraintlayout)
    implementation(libs.androidx.viewpager2)
    implementation(libs.androidx.recyclerview)
    testImplementation(libs.junit)
    androidTestImplementation(libs.androidx.junit)
    androidTestImplementation(libs.androidx.espresso.core)
}
