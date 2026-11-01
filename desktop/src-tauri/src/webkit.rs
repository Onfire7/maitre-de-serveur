//! WebKitGTK quirks, Linux only.
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
//! it is.
use glib::translate::ToGlibPtr;
use std::ffi::{c_char, c_void, CStr};
use webkit2gtk::WebViewExt;

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

/// Turns the ViewTransitions feature off for `view`'s settings; false when
/// this WebKitGTK has no feature API or no such feature.
pub fn disable_view_transitions(view: &webkit2gtk::WebView) -> bool {
    let Some(settings) = view.settings() else { return false };
    unsafe {
        let (Some(all), Some(len), Some(get), Some(ident), Some(set), Some(unref)) = (
            sym::<unsafe extern "C" fn() -> FeatureList>(c"webkit_settings_get_all_features"),
            sym::<unsafe extern "C" fn(FeatureList) -> usize>(c"webkit_feature_list_get_length"),
            sym::<unsafe extern "C" fn(FeatureList, usize) -> Feature>(c"webkit_feature_list_get"),
            sym::<unsafe extern "C" fn(Feature) -> *const c_char>(c"webkit_feature_get_identifier"),
            sym::<unsafe extern "C" fn(*mut c_void, Feature, i32)>(c"webkit_settings_set_feature_enabled"),
            sym::<unsafe extern "C" fn(FeatureList)>(c"webkit_feature_list_unref"),
        ) else {
            return false;
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
        found
    }
}
