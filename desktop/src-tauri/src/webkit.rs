//! WebKitGTK quirks, Linux only.
//!
//! WebKitGTK's default cookie policy refuses third-party cookies, and a
//! Service's page is a frame on a loopback origin of its own inside the
//! Console's origin: so a login kept in a cookie was set and dropped at
//! once (decision 292). `accept_every_cookie` turns the policy to always.
//!
//! WebKitGTK 2.52 dereferences a null accelerated backing store when a
//! page's first View Transition is the first thing to ask for accelerated
//! compositing and that store could not be made (bugs.webkit.org 321683).
//! With the DMA-BUF renderer off, as main.rs sets by default for NVIDIA,
//! the store is never made, so a Service page that uses the View
//! Transitions API (Memos does, on every route change) took the whole
//! Desktop down with SIGSEGV. Turning the ViewTransitions feature off makes
//! such pages see no `document.startViewTransition` and fall back to a plain
//! change, in the main frame and every Service's iframe alike.
//!
//! The feature API arrived in WebKitGTK 2.42 and the Desktop runs on 2.40,
//! so the symbols are looked up at run time and an older WebKit is left as
//! it is. A WebKit without the API, or with the API but no ViewTransitions
//! feature (it came as a preview feature in 2.46), has no View Transitions
//! to crash on; the note printed then names the version so a report from
//! another machine can be read.
use glib::translate::ToGlibPtr;
use std::ffi::{c_char, c_void, CStr};
use webkit2gtk::{CookieAcceptPolicy, CookieManagerExt, WebContextExt, WebViewExt};

type Feature = *mut c_void;
type FeatureList = *mut c_void;

unsafe fn sym<T: Copy>(name: &CStr) -> Option<T> {
    // T is always a function pointer, the same size as the data pointer.
    unsafe {
        let p = libc::dlsym(libc::RTLD_DEFAULT, name.as_ptr());
        if p.is_null() {
            None
        } else {
            Some(std::mem::transmute_copy::<*mut c_void, T>(&p))
        }
    }
}

/// The running WebKitGTK's version, as "2.52.6".
pub fn version() -> String {
    unsafe {
        use webkit2gtk::ffi::{webkit_get_major_version, webkit_get_micro_version, webkit_get_minor_version};
        format!("{}.{}.{}", webkit_get_major_version(), webkit_get_minor_version(), webkit_get_micro_version())
    }
}

/// Turns the ViewTransitions feature off for `view`'s settings. Err says why
/// it could not be: this WebKitGTK has no feature API (before 2.42) or no
/// ViewTransitions feature (before 2.46); either way it has no View
/// Transitions to turn off.
pub fn disable_view_transitions(view: &webkit2gtk::WebView) -> Result<(), &'static str> {
    let Some(settings) = view.settings() else { return Err("the view has no settings") };
    unsafe {
        let (Some(all), Some(len), Some(get), Some(ident), Some(set), Some(unref)) = (
            sym::<unsafe extern "C" fn() -> FeatureList>(c"webkit_settings_get_all_features"),
            sym::<unsafe extern "C" fn(FeatureList) -> usize>(c"webkit_feature_list_get_length"),
            sym::<unsafe extern "C" fn(FeatureList, usize) -> Feature>(c"webkit_feature_list_get"),
            sym::<unsafe extern "C" fn(Feature) -> *const c_char>(c"webkit_feature_get_identifier"),
            sym::<unsafe extern "C" fn(*mut c_void, Feature, i32)>(c"webkit_settings_set_feature_enabled"),
            sym::<unsafe extern "C" fn(FeatureList)>(c"webkit_feature_list_unref"),
        ) else {
            return Err("it has no feature API");
        };
        let list = all();
        let mut found = false;
        for i in 0..len(list) {
            let f = get(list, i);
            if CStr::from_ptr(ident(f)).to_bytes() == b"ViewTransitions" {
                let sp: *mut webkit2gtk::ffi::WebKitSettings = settings.to_glib_none().0;
                set(sp as *mut c_void, f, 0);
                found = true;
            }
        }
        unref(list);
        if found { Ok(()) } else { Err("it has no ViewTransitions feature") }
    }
}

/// Makes `view`'s web context accept every cookie, first- or third-party,
/// so a Service's frame keeps its login. Err says what was missing.
pub fn accept_every_cookie(view: &webkit2gtk::WebView) -> Result<(), &'static str> {
    let Some(context) = view.web_context() else { return Err("the view has no web context") };
    let Some(cookies) = context.cookie_manager() else { return Err("the web context has no cookie manager") };
    cookies.set_accept_policy(CookieAcceptPolicy::Always);
    Ok(())
}
